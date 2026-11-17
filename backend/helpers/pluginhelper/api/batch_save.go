/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/apache/devlake/core/context"
	"github.com/apache/devlake/core/dal"
	"github.com/apache/devlake/core/errors"
	"github.com/apache/devlake/core/log"
)

const (
	// batchSaveMaxRetries bounds how many extra attempts a flush makes after a
	// transient database locking error before giving up.
	batchSaveMaxRetries = 2
	// batchSaveRetryBackoff is multiplied by the attempt number for a simple
	// linear backoff between retries.
	batchSaveRetryBackoff = 100 * time.Millisecond
)

// BatchSave performs multiple records persistence of a specific type in one sql query to improve the performance
type BatchSave struct {
	basicRes context.BasicRes
	log      log.Logger
	db       dal.Dal
	slotType reflect.Type
	// slots can not be []interface{}, because gorm wouldn't take it
	// I'm guessing the reason is the type information lost when converted to interface{}
	slots      reflect.Value
	current    int
	size       int
	valueIndex map[string]int
	primaryKey []reflect.StructField
	tableName  string
	mutex      sync.Mutex
	lastErr    errors.Error
}

// NewBatchSave creates a new BatchSave instance
func NewBatchSave(basicRes context.BasicRes, slotType reflect.Type, size int, tableName ...string) (*BatchSave, errors.Error) {
	if slotType.Kind() != reflect.Ptr {
		panic(errors.Default.New("slotType must be a pointer"))
	}
	db := basicRes.GetDal()
	primaryKey := db.GetPrimaryKeyFields(slotType)
	// check if it has primaryKey
	if len(primaryKey) == 0 {
		return nil, errors.Default.New(fmt.Sprintf("%s no primary key", slotType.String()))
	}
	tn := ""
	if len(tableName) == 1 {
		tn = tableName[0]
	}

	logger := basicRes.GetLogger().Nested(slotType.String())
	return &BatchSave{
		basicRes:   basicRes,
		log:        logger,
		db:         db,
		slotType:   slotType,
		slots:      reflect.MakeSlice(reflect.SliceOf(slotType), size, size),
		size:       size,
		valueIndex: make(map[string]int),
		primaryKey: primaryKey,
		tableName:  tn,
	}, nil
}

// Add record to cache. BatchSave would flush them into Database when cache is max out
func (c *BatchSave) Add(slot interface{}) errors.Error {
	// type checking
	if reflect.TypeOf(slot) != c.slotType {
		return errors.Default.New("sub cache type mismatched")
	}
	if reflect.ValueOf(slot).Kind() != reflect.Ptr {
		return errors.Default.New("slot is not a pointer")
	}
	if c.lastErr != nil {
		return errors.Default.Wrap(c.lastErr, "add slot failed due to previous err")
	}
	stripZeroByte(slot)
	// deduplication
	key := getKeyValue(slot, c.primaryKey)
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if key != "" {
		if index, ok := c.valueIndex[key]; !ok {
			c.valueIndex[key] = c.current
		} else {
			c.slots.Index(index).Set(reflect.ValueOf(slot))
			return nil
		}
	}
	c.slots.Index(c.current).Set(reflect.ValueOf(slot))
	c.current++
	// flush out into database if maxed out
	if c.current == c.size {
		return c.flushWithoutLocking()
	} else if c.current%100 == 0 {
		c.log.Debug("batch save current: %d", c.current)
	}
	return nil
}

// Flush save cached records into database, even if cache is not maxed out
func (c *BatchSave) Flush() errors.Error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.flushWithoutLocking()
}

func (c *BatchSave) flushWithoutLocking() errors.Error {
	if c.current == 0 {
		return nil
	}
	clauses := make([]dal.Clause, 0)
	if c.tableName != "" {
		clauses = append(clauses, dal.From(c.tableName))
	}
	err := c.createOrUpdateWithRetry(c.slots.Slice(0, c.current).Interface(), clauses)
	if err != nil {
		c.lastErr = err
		return err
	}
	c.log.Debug("batch save flush total %d records to database", c.current)
	c.current = 0
	c.valueIndex = make(map[string]int)
	return nil
}

// createOrUpdateWithRetry upserts the batch, retrying a bounded number of times
// when the database reports a transient locking error. Concurrent tasks writing
// overlapping rows can trip InnoDB deadlocks (MySQL error 1213) or lock-wait
// timeouts; the batch is an idempotent upsert, so following the database's own
// advice to "try restarting transaction" is safe and avoids failing a whole
// subtask over a momentary lock cycle.
func (c *BatchSave) createOrUpdateWithRetry(entity interface{}, clauses []dal.Clause) errors.Error {
	var err errors.Error
	for attempt := 0; ; attempt++ {
		err = c.db.CreateOrUpdate(entity, clauses...)
		if err == nil || attempt >= batchSaveMaxRetries || !isRetryableDBError(err) {
			return err
		}
		c.log.Warn(err, "batch save on %s hit a transient lock error, retrying (%d/%d)", c.slotType.String(), attempt+1, batchSaveMaxRetries)
		time.Sleep(batchSaveRetryBackoff * time.Duration(attempt+1))
	}
}

// isRetryableDBError reports whether err is a transient database locking error
// that is safe to retry: an InnoDB deadlock or lock-wait timeout on MySQL, or a
// serialization failure or deadlock on Postgres. Detection is by message because
// the driver error is already wrapped by the time it reaches here.
func isRetryableDBError(err errors.Error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadlock") ||
		strings.Contains(msg, "lock wait timeout") ||
		strings.Contains(msg, "could not serialize access")
}

// Close would flush the cache and release resources
func (c *BatchSave) Close() errors.Error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.current > 0 {
		return c.flushWithoutLocking()
	}
	return nil
}

func getKeyValue(iface interface{}, primaryKey []reflect.StructField) string {
	var ss []string
	ifv := reflect.ValueOf(iface)
	if ifv.Kind() == reflect.Ptr {
		ifv = ifv.Elem()
	}
	for _, key := range primaryKey {
		v := ifv.FieldByName(key.Name)
		s := fmt.Sprintf("%v", v.Interface())
		if s != "" {
			ss = append(ss, s)
		}
	}
	return strings.Join(ss, ":")
}

func stripZeroByte(ifc interface{}) {
	v := reflect.ValueOf(ifc).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Type() == reflect.TypeOf("") {
			stripped := strings.ReplaceAll(v.Field(i).String(), "\u0000", "")
			v.Field(i).Set(reflect.ValueOf(stripped))
		}
	}
}
