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
	"reflect"
	"testing"

	"github.com/apache/devlake/core/errors"
	"github.com/apache/devlake/helpers/unithelper"
	mockcontext "github.com/apache/devlake/mocks/core/context"
	mockdal "github.com/apache/devlake/mocks/core/dal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func Test_stripZeroByte(t *testing.T) {
	type foo struct {
		Field      string
		FromString string
		ToString   string
		From       *string
	}
	from := "Earth\u0000"
	type args struct {
		ifc interface{}
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "Test_stripZeroByte",
			args: args{
				ifc: &foo{
					Field:      "home\u0000",
					FromString: "Earth",
					ToString:   "Mars\u0000",
					From:       &from,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripZeroByte(tt.args.ifc)
			if tt.args.ifc.(*foo).Field != "home" {
				t.Errorf("stripZeroByte() = %v, want %v", tt.args.ifc.(*foo).Field, "home")
			}
			if tt.args.ifc.(*foo).FromString != "Earth" {
				t.Errorf("stripZeroByte() = %v, want %v", tt.args.ifc.(*foo).FromString, "Earth")
			}
			if tt.args.ifc.(*foo).ToString != "Mars" {
				t.Errorf("stripZeroByte() = %v, want %v", tt.args.ifc.(*foo).ToString, "Mars")
			}
			if *tt.args.ifc.(*foo).From != from {
				t.Errorf("stripZeroByte() = %v, want %v", *tt.args.ifc.(*foo).From, "Earth")
			}
		})
	}
}

func Test_isRetryableDBError(t *testing.T) {
	cases := []struct {
		name string
		err  errors.Error
		want bool
	}{
		{"nil", nil, false},
		{"mysql deadlock", errors.Default.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction"), true},
		{"mysql lock wait timeout", errors.Default.New("Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction"), true},
		{"postgres serialization", errors.Default.New("ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)"), true},
		{"postgres deadlock", errors.Default.New("ERROR: deadlock detected (SQLSTATE 40P01)"), true},
		{"unrelated duplicate key", errors.Default.New("Error 1062 (23000): Duplicate entry 'x' for key 'PRIMARY'"), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableDBError(tt.err))
		})
	}
}

type retryRow struct {
	ID string `gorm:"primaryKey"`
}

func newMockBatchSave(t *testing.T, mockDal *mockdal.Dal) *BatchSave {
	logger := unithelper.DummyLogger()
	// the retry path logs a Warn with extra args; DummyLogger only stubs the
	// 2-arg form, so allow the variadic form used here.
	logger.On("Warn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	mockRes := new(mockcontext.BasicRes)
	mockRes.On("GetDal").Return(mockDal)
	mockRes.On("GetLogger").Return(logger)
	mockDal.On("GetPrimaryKeyFields", mock.Anything).Return(
		[]reflect.StructField{{Name: "ID", Type: reflect.TypeOf("")}},
	)
	bs, err := NewBatchSave(mockRes, reflect.TypeOf(&retryRow{}), 10)
	assert.Nil(t, err)
	return bs
}

func TestBatchSave_retriesTransientLockThenSucceeds(t *testing.T) {
	mockDal := new(mockdal.Dal)
	bs := newMockBatchSave(t, mockDal)
	deadlock := errors.Default.New("Error 1213 (40001): Deadlock found; try restarting transaction")
	mockDal.On("CreateOrUpdate", mock.Anything).Return(deadlock).Twice()
	mockDal.On("CreateOrUpdate", mock.Anything).Return(nil).Once()

	assert.Nil(t, bs.Add(&retryRow{ID: "1"}))
	assert.Nil(t, bs.Flush())
	mockDal.AssertNumberOfCalls(t, "CreateOrUpdate", 3)
}

func TestBatchSave_givesUpAfterMaxRetries(t *testing.T) {
	mockDal := new(mockdal.Dal)
	bs := newMockBatchSave(t, mockDal)
	deadlock := errors.Default.New("Error 1213 (40001): Deadlock found; try restarting transaction")
	mockDal.On("CreateOrUpdate", mock.Anything).Return(deadlock)

	assert.Nil(t, bs.Add(&retryRow{ID: "1"}))
	assert.NotNil(t, bs.Flush())
	mockDal.AssertNumberOfCalls(t, "CreateOrUpdate", 1+batchSaveMaxRetries)
}

func TestBatchSave_doesNotRetryNonLockError(t *testing.T) {
	mockDal := new(mockdal.Dal)
	bs := newMockBatchSave(t, mockDal)
	dup := errors.Default.New("Error 1062 (23000): Duplicate entry 'x' for key 'PRIMARY'")
	mockDal.On("CreateOrUpdate", mock.Anything).Return(dup).Once()

	assert.Nil(t, bs.Add(&retryRow{ID: "1"}))
	assert.NotNil(t, bs.Flush())
	mockDal.AssertNumberOfCalls(t, "CreateOrUpdate", 1)
}
