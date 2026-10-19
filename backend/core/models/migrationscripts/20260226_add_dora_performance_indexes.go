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
	"time"

	"github.com/apache/devlake/core/context"
	"github.com/apache/devlake/core/errors"
	"github.com/apache/devlake/core/plugin"
	"github.com/apache/devlake/helpers/migrationhelper"
)

var _ plugin.MigrationScript = (*addDoraPerformanceIndexes)(nil)

type projectPrMetric20260226 struct {
	DeploymentCommitId string `gorm:"index"`
}

func (projectPrMetric20260226) TableName() string {
	return "project_pr_metrics"
}

type projectIncidentDeploymentRelationship20260226 struct {
	DeploymentId string `gorm:"index"`
}

func (projectIncidentDeploymentRelationship20260226) TableName() string {
	return "project_incident_deployment_relationships"
}

type cicdDeploymentCommit20260226 struct {
	Result       string     `gorm:"index:idx_result_environment_scope_id_finished_date"`
	Environment  string     `gorm:"index:idx_result_environment_scope_id_finished_date"`
	CicdScopeId  string     `gorm:"index:idx_result_environment_scope_id_finished_date"`
	FinishedDate *time.Time `gorm:"index:idx_result_environment_scope_id_finished_date"`
}

func (cicdDeploymentCommit20260226) TableName() string {
	return "cicd_deployment_commits"
}

type addDoraPerformanceIndexes struct{}

func (*addDoraPerformanceIndexes) Up(basicRes context.BasicRes) errors.Error {
	err := migrationhelper.AutoMigrateTables(
		basicRes,
		&projectPrMetric20260226{},
		&projectIncidentDeploymentRelationship20260226{},
		&cicdDeploymentCommit20260226{},
	)
	if err != nil {
		return err
	}

	// project_mapping requires raw SQL because Postgres rejects AutoMigrate
	// on columns that are part of the primary key.
	db := basicRes.GetDal()
	switch db.Dialect() {
	case "mysql":
		return db.Exec("CREATE INDEX idx_rowid_table_project_name ON project_mapping(row_id, `table`, project_name)")
	case "postgres":
		return db.Exec(`CREATE INDEX idx_rowid_table_project_name ON project_mapping(row_id, "table", project_name)`)
	default:
		return nil
	}
}

func (*addDoraPerformanceIndexes) Version() uint64 {
	return 20260226170000
}

func (*addDoraPerformanceIndexes) Name() string {
	return "add indexes to improve DORA dashboard query performance"
}
