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

package services

import (
	"testing"

	"github.com/apache/devlake/core/errors"
	"github.com/apache/devlake/core/models"
	mockdal "github.com/apache/devlake/mocks/core/dal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func Test_isTransientLockError(t *testing.T) {
	cases := []struct {
		name string
		err  errors.Error
		want bool
	}{
		{"nil", nil, false},
		{"mysql deadlock", errors.Default.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction"), true},
		{"mysql lock wait timeout", errors.Default.New("Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction"), true},
		{"postgres serialization", errors.Default.New("pq: could not serialize access due to concurrent update"), true},
		{"unrelated error", errors.Default.New("Error 1146 (42S02): Table 'devlake._devlake_pipelines' doesn't exist"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isTransientLockError(tc.err))
		})
	}
}

// withMockDB swaps the package-level db for a mock and restores it afterwards.
func withMockDB(t *testing.T) *mockdal.Dal {
	t.Helper()
	orig := db
	m := mockdal.NewDal(t)
	db = m
	t.Cleanup(func() { db = orig })
	return m
}

func Test_markInterruptedPipelineAs_resetsBothEntities(t *testing.T) {
	m := withMockDB(t)
	m.On("UpdateColumns", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	assert.NotPanics(t, func() { markInterruptedPipelineAs(models.TASK_RESUME) })
	// once for pipelines, once for tasks
	m.AssertNumberOfCalls(t, "UpdateColumns", 2)
}

// Test_markInterruptedPipelineAs_continuesOnLockContention is the regression test
// for the CrashLoopBackOff: a lock-wait timeout at startup must be tolerated, not
// panic, so the server can finish booting and serve its health probe.
func Test_markInterruptedPipelineAs_continuesOnLockContention(t *testing.T) {
	m := withMockDB(t)
	lockErr := errors.Default.New("Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction")
	m.On("UpdateColumns", mock.Anything, mock.Anything, mock.Anything).Return(lockErr)

	assert.NotPanics(t, func() { markInterruptedPipelineAs(models.TASK_RESUME) })
	// both entities are still attempted even though the first one failed
	m.AssertNumberOfCalls(t, "UpdateColumns", 2)
}

func Test_markInterruptedPipelineAs_panicsOnNonTransientError(t *testing.T) {
	m := withMockDB(t)
	fatalErr := errors.Default.New("Error 1146 (42S02): Table 'devlake._devlake_pipelines' doesn't exist")
	m.On("UpdateColumns", mock.Anything, mock.Anything, mock.Anything).Return(fatalErr)

	assert.Panics(t, func() { markInterruptedPipelineAs(models.TASK_FAILED) })
}
