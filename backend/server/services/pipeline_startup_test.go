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
	"fmt"
	"testing"

	"github.com/apache/devlake/core/dal"
	"github.com/apache/devlake/core/models"
	mockdal "github.com/apache/devlake/mocks/core/dal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// The startup reset of interrupted pipelines and tasks must filter on
// finished_at: it is indexed on both tables while status is not, and on large
// installs a full scan of _devlake_tasks takes longer than the startup probe.
func TestMarkInterruptedPipelineAsFiltersOnFinishedAt(t *testing.T) {
	mockDB := mockdal.NewDal(t)
	previousDB := db
	db = mockDB
	t.Cleanup(func() { db = previousDB })

	var updatedTables []string
	var wheres []dal.DalClause
	mockDB.On("UpdateColumns", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		updatedTables = append(updatedTables, fmt.Sprintf("%T", args.Get(0)))
		assert.Equal(t, []dal.DalSet{{ColumnName: "status", Value: models.TASK_RESUME}}, args.Get(1))
		clauses := args.Get(2).([]dal.Clause)
		if assert.Len(t, clauses, 1) {
			assert.Equal(t, dal.WhereClause, clauses[0].Type)
			wheres = append(wheres, clauses[0].Data.(dal.DalClause))
		}
	}).Return(nil).Twice()

	markInterruptedPipelineAs(models.TASK_RESUME)

	assert.Equal(t, []string{"*models.Pipeline", "*models.Task"}, updatedTables)
	if assert.Len(t, wheres, 2) {
		for _, where := range wheres {
			assert.Equal(t, "status = ? AND finished_at IS NULL", where.Expr)
			assert.Equal(t, []interface{}{models.TASK_RUNNING}, where.Params)
		}
	}
}
