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

package migrationscripts

import (
	"github.com/apache/devlake/core/context"
	"github.com/apache/devlake/core/errors"
	"github.com/apache/devlake/core/plugin"
)

var _ plugin.MigrationScript = (*addProjectPrMetricsProjectNameIndex)(nil)

type addProjectPrMetricsProjectNameIndex struct{}

func (*addProjectPrMetricsProjectNameIndex) Up(basicRes context.BasicRes) errors.Error {
	// change_lead_time deletes a project's rows with
	// DELETE FROM project_pr_metrics WHERE project_name = ?. project_name is the
	// trailing column of the (id, project_name) primary key, so without a
	// dedicated index that delete scans the whole table and takes wide locks,
	// which deadlocks when several projects are calculated concurrently.
	// A standalone index on project_name lets the delete touch only that
	// project's rows. Raw SQL is required because Postgres rejects AutoMigrate
	// indexes on columns that are part of the primary key.
	return basicRes.GetDal().Exec(
		"CREATE INDEX idx_project_pr_metrics_project_name ON project_pr_metrics(project_name)",
	)
}

func (*addProjectPrMetricsProjectNameIndex) Version() uint64 {
	return 20261116000001
}

func (*addProjectPrMetricsProjectNameIndex) Name() string {
	return "add index on project_pr_metrics.project_name to avoid full-table locks"
}
