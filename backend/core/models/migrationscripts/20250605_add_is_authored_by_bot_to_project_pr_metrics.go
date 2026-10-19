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

var _ plugin.MigrationScript = (*addIsAuthoredByBotToProjectPrMetric)(nil)

type projectPrMetric20250617 struct {
	IsAuthoredByBot bool
}

func (projectPrMetric20250617) TableName() string {
	return "project_pr_metrics"
}

type addIsAuthoredByBotToProjectPrMetric struct{}

func (*addIsAuthoredByBotToProjectPrMetric) Up(basicRes context.BasicRes) errors.Error {
	db := basicRes.GetDal()
	if err := db.AutoMigrate(&projectPrMetric20250617{}); err != nil {
		return err
	}
	return nil
}

func (*addIsAuthoredByBotToProjectPrMetric) Version() uint64 {
	return 20250617123431
}

func (*addIsAuthoredByBotToProjectPrMetric) Name() string {
	return "add is_authored_by_bot to project_pr_metrics according to #8381"
}
