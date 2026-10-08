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

package e2e

import (
	"testing"

	"github.com/apache/incubator-devlake/core/models/common"
	"github.com/apache/incubator-devlake/core/models/domainlayer/code"
	"github.com/apache/incubator-devlake/core/models/domainlayer/crossdomain"
	"github.com/apache/incubator-devlake/core/models/domainlayer/devops"
	"github.com/apache/incubator-devlake/helpers/e2ehelper"
	"github.com/apache/incubator-devlake/plugins/dora/impl"
	"github.com/apache/incubator-devlake/plugins/dora/tasks"
)

// Repo X moved from project A to project B. Lead time must stay with the project
// whose cicd scope first shipped each PR: A keeps what it shipped before the move,
// B gets only what its own scope shipped, and nothing is counted twice. A
// monorepo mapped to both projects splits the same way. A deployment on a scope
// mapped to no project is ignored.
func TestCalculateCLTimeWebhookOwnedDataFlow(t *testing.T) {
	var plugin impl.Dora
	dataflowTester := e2ehelper.NewDataFlowTester(t, "dora", plugin)

	t.Setenv("ENABLE_BOT_FILTERING", "false")

	const dir = "./change_lead_time_webhook_owned/"
	dataflowTester.ImportCsvIntoTabler(dir+"project_mapping.csv", &crossdomain.ProjectMapping{})
	dataflowTester.ImportCsvIntoTabler(dir+"repos.csv", &code.Repo{})
	dataflowTester.ImportCsvIntoTabler(dir+"cicd_scopes.csv", &devops.CicdScope{})
	dataflowTester.ImportCsvIntoTabler(dir+"repo_commits.csv", &code.RepoCommit{})
	dataflowTester.ImportCsvIntoTabler(dir+"commit_parents.csv", &code.CommitParent{})
	dataflowTester.ImportCsvIntoTabler(dir+"cicd_deployment_commits.csv", &devops.CicdDeploymentCommit{})
	dataflowTester.ImportCsvIntoTabler(dir+"pull_requests.csv", &code.PullRequest{})
	dataflowTester.ImportCsvIntoTabler(dir+"pull_request_commits.csv", &code.PullRequestCommit{})
	dataflowTester.ImportCsvIntoTabler(dir+"pull_request_comments.csv", &code.PullRequestComment{})
	dataflowTester.ImportCsvIntoTabler(dir+"accounts.csv", &crossdomain.Account{})

	dataflowTester.FlushTabler(&crossdomain.ProjectPrMetric{})

	// Project A first: it keeps the PRs its scope shipped, including repo X
	// which is no longer mapped to it.
	dataflowTester.Subtask(tasks.CalculateChangeLeadTimeMeta, &tasks.DoraTaskData{
		Options: &tasks.DoraOptions{ProjectName: "projectA"},
	})
	dataflowTester.VerifyTableWithOptions(&crossdomain.ProjectPrMetric{}, e2ehelper.TableOptions{
		CSVRelPath:  dir + "project_pr_metrics_project_a.csv",
		IgnoreTypes: []interface{}{common.NoPKModel{}},
	})

	// Project B: only what its own scope shipped, plus never-deployed PRs of
	// repos it maps. It must leave project A's rows alone.
	dataflowTester.Subtask(tasks.CalculateChangeLeadTimeMeta, &tasks.DoraTaskData{
		Options: &tasks.DoraOptions{ProjectName: "projectB"},
	})
	dataflowTester.VerifyTableWithOptions(&crossdomain.ProjectPrMetric{}, e2ehelper.TableOptions{
		CSVRelPath:  dir + "project_pr_metrics_both.csv",
		IgnoreTypes: []interface{}{common.NoPKModel{}},
	})
}
