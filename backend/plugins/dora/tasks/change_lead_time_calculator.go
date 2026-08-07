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

package tasks

import (
	"math"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/apache/incubator-devlake/core/config"
	"github.com/apache/incubator-devlake/core/dal"
	"github.com/apache/incubator-devlake/core/errors"
	"github.com/apache/incubator-devlake/core/models/domainlayer/code"
	"github.com/apache/incubator-devlake/core/models/domainlayer/crossdomain"
	"github.com/apache/incubator-devlake/core/models/domainlayer/devops"
	"github.com/apache/incubator-devlake/core/plugin"
	"github.com/apache/incubator-devlake/helpers/pluginhelper/api"
)

// CalculateChangeLeadTimeMeta contains metadata for the CalculateChangeLeadTime subtask.
var CalculateChangeLeadTimeMeta = plugin.SubTaskMeta{
	Name:             "calculateChangeLeadTime",
	EntryPoint:       CalculateChangeLeadTime,
	EnabledByDefault: true,
	Description:      "Calculate change lead time",
	DomainTypes:      []string{plugin.DOMAIN_TYPE_CICD, plugin.DOMAIN_TYPE_CODE},
}

// CalculateChangeLeadTime calculates change lead time for a project.
func CalculateChangeLeadTime(taskCtx plugin.SubTaskContext) errors.Error {
	// Get instances of the DAL and logger
	db := taskCtx.GetDal()
	logger := taskCtx.GetLogger()
	data := taskCtx.GetData().(*DoraTaskData)
	// Clear previous results from the project
	err := db.Exec("DELETE FROM project_pr_metrics WHERE project_name = ? ", data.Options.ProjectName)
	if err != nil {
		return errors.Default.Wrap(err, "error deleting previous project_pr_metrics")
	}

	// Get env vars for bot filtering
	cfg := config.GetConfig()
	enableBotFiltering := cfg.GetBool("ENABLE_BOT_FILTERING")
	botFilteringPattern := cfg.GetString("BOT_FILTERING_PATTERN")

	// Precompile bot filtering regex if enabled and pattern is set
	var botFilteringRegex *regexp.Regexp
	if enableBotFiltering && botFilteringPattern == "" {
		logger.Warn(nil, "ENABLE_BOT_FILTERING is true but BOT_FILTERING_PATTERN is empty; no PRs will be marked as bot-authored")
	}
	if enableBotFiltering && botFilteringPattern != "" {
		var err error
		botFilteringRegex, err = regexp.Compile(botFilteringPattern)
		if err != nil {
			logger.Warn(err, "Invalid bot filtering pattern: %s", botFilteringPattern)
			botFilteringRegex = nil
		}
	}

	// Batch fetch all required data upfront for better performance
	startTime := time.Now()
	logger.Info("Batch fetching data for project: %s", data.Options.ProjectName)

	firstCommitsMap, err := batchFetchFirstCommits(data.Options.ProjectName, db)
	if err != nil {
		return errors.Default.Wrap(err, "failed to batch fetch first commits")
	}
	logger.Info("Fetched %d first commits in %v", len(firstCommitsMap), time.Since(startTime))

	// CI bots comment within seconds of a PR opening; counting them as the first
	// review zeroes pr_pickup_time. Exclude their accounts from the review query.
	botAccountIds, err := loadBotAccountIds(db, botFilteringRegex)
	if err != nil {
		return errors.Default.Wrap(err, "failed to load bot account ids")
	}

	reviewStartTime := time.Now()
	firstReviewsMap, err := batchFetchFirstReviews(data.Options.ProjectName, db, botAccountIds)
	if err != nil {
		return errors.Default.Wrap(err, "failed to batch fetch first reviews")
	}
	logger.Info("Fetched %d first reviews in %v", len(firstReviewsMap), time.Since(reviewStartTime))

	deploymentStartTime := time.Now()
	deploymentsMap, err := batchFetchDeployments(data.Options.ProjectName, db)
	if err != nil {
		return errors.Default.Wrap(err, "failed to batch fetch deployments")
	}
	logger.Info("Fetched %d deployments in %v", len(deploymentsMap), time.Since(deploymentStartTime))
	logger.Info("Total batch fetch time: %v", time.Since(startTime))

	// Get pull requests by repo project_name
	var clauses = []dal.Clause{
		dal.Select("pr.id, pr.pull_request_key, pr.author_id, pr.author_name, pr.merge_commit_sha, pr.created_date, pr.merged_date"),
		dal.From("pull_requests pr"),
		dal.Join(`LEFT JOIN project_mapping pm ON (pm.row_id = pr.base_repo_id)`),
		dal.Where("pr.merged_date IS NOT NULL AND pm.project_name = ? AND pm.table = 'repos'", data.Options.ProjectName),
	}
	cursor, err := db.Cursor(clauses...)
	if err != nil {
		return err
	}
	defer cursor.Close()

	converter, err := api.NewDataConverter(api.DataConverterArgs{
		RawDataSubTaskArgs: api.RawDataSubTaskArgs{
			Ctx: taskCtx,
			// table and params are essential for deleting data from the target table
			Params: DoraApiParams{
				ProjectName: data.Options.ProjectName,
			},
			Table: "pull_requests",
		},
		BatchSize:    100,
		InputRowType: reflect.TypeOf(code.PullRequest{}),
		Input:        cursor,
		Convert: func(inputRow interface{}) ([]interface{}, errors.Error) {
			pr := inputRow.(*code.PullRequest)
			// Initialize a new ProjectPrMetric
			projectPrMetric := &crossdomain.ProjectPrMetric{}
			projectPrMetric.Id = pr.Id
			projectPrMetric.ProjectName = data.Options.ProjectName

			// Get the first commit for the PR from batch-fetched map
			firstCommit := firstCommitsMap[pr.Id]
			// Calculate PR coding time
			if firstCommit != nil {
				projectPrMetric.PrCodingTime = computeTimeSpan(&firstCommit.CommitAuthoredDate, &pr.CreatedDate)
				projectPrMetric.FirstCommitSha = firstCommit.CommitSha
				projectPrMetric.FirstCommitAuthoredDate = &firstCommit.CommitAuthoredDate
			}
			projectPrMetric.IsAuthoredByBot = matchesBotFilter(botFilteringRegex, pr.AuthorName)

			// Get the first review for the PR from batch-fetched map
			firstReview := firstReviewsMap[pr.Id]
			// Calculate PR pickup time and PR review time
			prDuring := computeTimeSpan(&pr.CreatedDate, pr.MergedDate)
			if firstReview != nil {
				projectPrMetric.PrPickupTime = computeTimeSpan(&pr.CreatedDate, &firstReview.CreatedDate)
				projectPrMetric.PrReviewTime = computeTimeSpan(&firstReview.CreatedDate, pr.MergedDate)
				projectPrMetric.FirstReviewId = firstReview.Id
				projectPrMetric.FirstCommentDate = &firstReview.CreatedDate
			}

			projectPrMetric.PrCreatedDate = &pr.CreatedDate
			projectPrMetric.PrMergedDate = pr.MergedDate

			// Get the deployment for the PR from batch-fetched map
			deployment := deploymentsMap[pr.MergeCommitSha]

			// Calculate PR deploy time
			if deployment != nil && deployment.FinishedDate != nil {
				projectPrMetric.PrDeployTime = computeTimeSpan(pr.MergedDate, deployment.FinishedDate)
				projectPrMetric.DeploymentCommitId = deployment.Id
				projectPrMetric.PrDeployedDate = deployment.FinishedDate
			} else {
				logger.Debug("deploy time of pr %v is nil\n", pr.PullRequestKey)
			}

			// Calculate PR cycle time
			var cycleTime int64
			if projectPrMetric.PrCodingTime != nil {
				cycleTime += *projectPrMetric.PrCodingTime
			}
			if prDuring != nil {
				cycleTime += *prDuring
			}
			if projectPrMetric.PrDeployTime != nil {
				cycleTime += *projectPrMetric.PrDeployTime
			}
			projectPrMetric.PrCycleTime = &cycleTime

			// Return the projectPrMetric
			return []interface{}{projectPrMetric}, nil
		},
	})
	if err != nil {
		return err
	}
	// Execute the data converter
	return converter.Execute()
}

func computeTimeSpan(start, end *time.Time) *int64 {
	if start == nil || end == nil {
		return nil
	}
	span := end.Sub(*start)
	minutes := int64(math.Ceil(span.Minutes()))
	if minutes < 0 {
		return nil
	}
	return &minutes
}

func matchesBotFilter(botFilterRegex *regexp.Regexp, name string) bool {
	if botFilterRegex == nil {
		return false
	}
	return botFilterRegex.MatchString(name)
}

// botAccountIdSet returns the sorted ids of accounts whose user_name matches the
// bot filter. Split from the DB access so it can be unit-tested.
func botAccountIdSet(accounts []*crossdomain.Account, botFilterRegex *regexp.Regexp) []string {
	if botFilterRegex == nil {
		return nil
	}
	var ids []string
	for _, account := range accounts {
		if matchesBotFilter(botFilterRegex, account.UserName) {
			ids = append(ids, account.Id)
		}
	}
	sort.Strings(ids)
	return ids
}

// loadBotAccountIds resolves bot account ids by matching account user_names
// against the bot filter. The regex runs in Go, not SQL, so the semantics match
// the PR-author filter and stay portable across databases. Nil regex: no exclusion.
func loadBotAccountIds(db dal.Dal, botFilterRegex *regexp.Regexp) ([]string, errors.Error) {
	if botFilterRegex == nil {
		return nil, nil
	}
	var accounts []*crossdomain.Account
	if err := db.All(&accounts, dal.Select("id, user_name"), dal.From("accounts")); err != nil {
		return nil, err
	}
	return botAccountIdSet(accounts, botFilterRegex), nil
}

// commitParentEdge is a lightweight row used to load the commit graph (child -> parent edges)
// for commit-ancestry based deployment attribution.
type commitParentEdge struct {
	CommitSha       string `gorm:"column:commit_sha"`
	ParentCommitSha string `gorm:"column:parent_commit_sha"`
}

// batchFetchFirstCommits retrieves the first commit for all pull requests in the given project.
// Returns a map indexed by PR ID for O(1) lookup performance.
//
// The query uses a subquery to find the minimum commit_authored_date for each PR,
// then joins back to get the full commit record. This is more efficient than
// fetching all commits and filtering in memory.
func batchFetchFirstCommits(projectName string, db dal.Dal) (map[string]*code.PullRequestCommit, errors.Error) {
	var results []*code.PullRequestCommit

	// Use a subquery to find the earliest commit for each PR, then join to get full commit details.
	// This avoids scanning all commits and is optimized by the database engine.
	err := db.All(
		&results,
		dal.Select("prc.*"),
		dal.From("pull_request_commits prc"),
		dal.Join(`INNER JOIN (
			SELECT pull_request_id, MIN(commit_authored_date) as min_date
			FROM pull_request_commits
			GROUP BY pull_request_id
		) first_commits ON prc.pull_request_id = first_commits.pull_request_id
		AND prc.commit_authored_date = first_commits.min_date`),
		dal.Join("INNER JOIN pull_requests pr ON pr.id = prc.pull_request_id"),
		dal.Join("LEFT JOIN project_mapping pm ON pm.row_id = pr.base_repo_id AND pm.table = 'repos'"),
		dal.Where("pm.project_name = ?", projectName),
		dal.Orderby("prc.pull_request_id, prc.commit_authored_date ASC"),
	)

	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to batch fetch first commits")
	}

	// Build the map for O(1) lookup by PR ID
	commitMap := make(map[string]*code.PullRequestCommit, len(results))
	for _, commit := range results {
		// Only keep the first commit if multiple commits have the same timestamp
		if _, exists := commitMap[commit.PullRequestId]; !exists {
			commitMap[commit.PullRequestId] = commit
		}
	}

	return commitMap, nil
}

// batchFetchFirstReviews retrieves the first review comment for all pull requests in the given project.
// Returns a map indexed by PR ID for O(1) lookup performance.
//
// The query uses a subquery to find the minimum created_date for each PR (excluding the PR author
// and any bot accounts), then joins back to get the full comment record.
func batchFetchFirstReviews(projectName string, db dal.Dal, botAccountIds []string) (map[string]*code.PullRequestComment, errors.Error) {
	var results []*code.PullRequestComment

	// Exclude bots inside the MIN() subquery (so a bot can't win the minimum) and
	// in the outer filter (so a bot can't ride a timestamp tie).
	botFilterSub, botFilterOuter := "", ""
	var subParams, outerParams []interface{}
	if len(botAccountIds) > 0 {
		botFilterSub = " AND prc2.account_id NOT IN (?)"
		subParams = append(subParams, botAccountIds)
		botFilterOuter = " AND prc.account_id NOT IN (?)"
		outerParams = append(outerParams, botAccountIds)
	}

	err := db.All(
		&results,
		dal.Select("prc.*"),
		dal.From("pull_request_comments prc"),
		dal.Join(`INNER JOIN (
			SELECT prc2.pull_request_id, MIN(prc2.created_date) as min_date
			FROM pull_request_comments prc2
			INNER JOIN pull_requests pr2 ON pr2.id = prc2.pull_request_id
			WHERE (pr2.author_id IS NULL OR pr2.author_id = '' OR prc2.account_id != pr2.author_id)`+botFilterSub+`
			GROUP BY prc2.pull_request_id
		) first_reviews ON prc.pull_request_id = first_reviews.pull_request_id
		AND prc.created_date = first_reviews.min_date`, subParams...),
		dal.Join("INNER JOIN pull_requests pr ON pr.id = prc.pull_request_id"),
		dal.Join("LEFT JOIN project_mapping pm ON pm.row_id = pr.base_repo_id AND pm.table = 'repos'"),
		dal.Where("pm.project_name = ? AND (pr.author_id IS NULL OR pr.author_id = '' OR prc.account_id != pr.author_id)"+botFilterOuter,
			append([]interface{}{projectName}, outerParams...)...),
		dal.Orderby("prc.pull_request_id, prc.created_date ASC"),
	)

	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to batch fetch first reviews")
	}

	// Build the map for O(1) lookup by PR ID
	reviewMap := make(map[string]*code.PullRequestComment, len(results))
	for _, review := range results {
		// Only keep the first review if multiple reviews have the same timestamp
		if _, exists := reviewMap[review.PullRequestId]; !exists {
			reviewMap[review.PullRequestId] = review
		}
	}

	return reviewMap, nil
}

// batchFetchDeployments maps each commit to the EARLIEST successful production deployment
// whose commit descends from it - i.e. the deployment that first shipped that commit.
// Returns a map indexed by commit SHA for O(1) lookup; the caller looks up a PR by its
// merge_commit_sha.
//
// This uses the commit-ancestry definition of "deployed": a commit is shipped by the first
// successful production deployment that has it as an ancestor in the git graph. It is computed
// by walking commit_parents from each deployment commit, processing deployments oldest-first
// and attributing every newly-reached (unclaimed) commit to that deployment. Pruning the walk
// at already-claimed commits keeps the whole pass O(commits + edges): once a commit is claimed
// by an earlier deployment, all of its ancestors were claimed in that same walk too.
//
// Why not a commits_diffs join: refdiff's incremental diff (old = prev_success) misses commits
// that reach production via a merge commit or after a superseded Stop/FAILURE deployment
// (under-linking), while matching any commits_diffs row absorbs the rooted empty-baseline
// diffs that Stop/FAILURE deployments generate and collapses whole histories onto one
// deployment (over-linking). Walking the real commit graph avoids both.
//
// Note: requires a complete commit graph. Where gitextractor shallow-cloned (missing commits),
// the walk stops early; the full-clone collection must run first. Also, the first deployment we
// have recorded for a repo claims its entire prior history, so PRs merged before deployment
// tracking began (or across a tracking gap, e.g. a repo migration) can attach to a later
// deployment with an inflated lead time - handle those via scope/guardrails, not here.
func batchFetchDeployments(projectName string, db dal.Dal) (map[string]*devops.CicdDeploymentCommit, errors.Error) {
	// 1. All successful production deployments for the project, earliest first.
	var deployments []*devops.CicdDeploymentCommit
	err := db.All(
		&deployments,
		dal.Select("dc.*"),
		dal.From("cicd_deployment_commits dc"),
		dal.Join("LEFT JOIN project_mapping pm ON pm.table = 'cicd_scopes' AND pm.row_id = dc.cicd_scope_id"),
		dal.Where("dc.environment = 'PRODUCTION'"), // TODO: remove this when multi-environment is supported
		dal.Where("dc.result = ? AND pm.project_name = ?", devops.RESULT_SUCCESS, projectName),
		dal.Orderby("dc.finished_date ASC, dc.id ASC"),
	)
	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to fetch deployments")
	}

	// 2. The project's commit graph as child -> parents adjacency.
	var edges []*commitParentEdge
	err = db.All(
		&edges,
		dal.Select("cp.commit_sha, cp.parent_commit_sha"),
		dal.From("commit_parents cp"),
		dal.Join("INNER JOIN repo_commits rc ON rc.commit_sha = cp.commit_sha"),
		dal.Join("INNER JOIN project_mapping pm ON pm.table = 'repos' AND pm.row_id = rc.repo_id"),
		dal.Where("pm.project_name = ?", projectName),
	)
	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to fetch commit graph")
	}
	parents := make(map[string][]string, len(edges))
	for _, e := range edges {
		parents[e.CommitSha] = append(parents[e.CommitSha], e.ParentCommitSha)
	}

	// 3. Attribute each commit to the first deployment that reaches it (oldest deployment first).
	return attributeCommitsToDeployments(deployments, parents), nil
}

// attributeCommitsToDeployments maps every commit in the graph to the deployment that
// first shipped it: the earliest successful production deployment that has the commit as
// an ancestor. deployments must be ordered oldest-first; parents is the commit graph as a
// child -> parents adjacency list.
//
// For each deployment (oldest first) it walks the deployment commit's ancestry, claiming
// every commit not yet owned by an earlier deployment. Reaching an already-claimed commit
// prunes the walk: that commit and all of its ancestors were necessarily reached by an
// earlier (older) deployment, so they keep their existing owner. This is O(commits + edges).
func attributeCommitsToDeployments(
	deployments []*devops.CicdDeploymentCommit,
	parents map[string][]string,
) map[string]*devops.CicdDeploymentCommit {
	deploymentMap := make(map[string]*devops.CicdDeploymentCommit)
	for _, deployment := range deployments {
		if deployment.CommitSha == "" {
			continue
		}
		stack := []string{deployment.CommitSha}
		for len(stack) > 0 {
			sha := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, claimed := deploymentMap[sha]; claimed {
				// Already attributed to an earlier deployment; its ancestors are too -> prune.
				continue
			}
			deploymentMap[sha] = deployment
			stack = append(stack, parents[sha]...)
		}
	}
	return deploymentMap
}
