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
	"strings"
	"time"

	"github.com/apache/incubator-devlake/core/config"
	"github.com/apache/incubator-devlake/core/dal"
	"github.com/apache/incubator-devlake/core/errors"
	"github.com/apache/incubator-devlake/core/log"
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
//
// PRs are credited to the project whose webhook first shipped them, not to the
// project that currently maps the repo; never-deployed PRs stay with the project
// that maps the repo, without deploy fields. See decidePrEmit and loadLeadTimeScope.
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

	scope, err := loadLeadTimeScope(data.Options.ProjectName, db, logger)
	if err != nil {
		return errors.Default.Wrap(err, "failed to load lead time scope")
	}
	logger.Info("Lead time scope for %s: %d scopes, %d owned repos, %d candidate repos, %d deployment urls",
		scope.ProjectName, len(scope.ScopeIds), len(scope.OwnedRepoIds), len(scope.CandidateRepoIds), len(scope.CandidateDeployUrls))
	if len(scope.CandidateRepoIds) == 0 {
		logger.Info("project %s maps no repos and its webhooks shipped none; nothing to calculate", scope.ProjectName)
		return nil
	}

	// Batch fetch all required data upfront for better performance
	startTime := time.Now()
	logger.Info("Batch fetching data for project: %s", data.Options.ProjectName)

	firstCommitsMap, err := batchFetchFirstCommits(scope.CandidateRepoIds, db)
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
	firstReviewsMap, err := batchFetchFirstReviews(scope.CandidateRepoIds, db, botAccountIds)
	if err != nil {
		return errors.Default.Wrap(err, "failed to batch fetch first reviews")
	}
	logger.Info("Fetched %d first reviews in %v", len(firstReviewsMap), time.Since(reviewStartTime))

	deploymentStartTime := time.Now()
	deploymentsMap, err := batchFetchDeployments(scope, db)
	if err != nil {
		return errors.Default.Wrap(err, "failed to batch fetch deployments")
	}
	logger.Info("Fetched %d deployments in %v", len(deploymentsMap), time.Since(deploymentStartTime))
	logger.Info("Total batch fetch time: %v", time.Since(startTime))

	// Merged pull requests of every candidate repo; the converter decides per PR
	// whether this project emits it.
	var clauses = []dal.Clause{
		dal.Select("pr.id, pr.base_repo_id, pr.pull_request_key, pr.author_id, pr.author_name, pr.merge_commit_sha, pr.created_date, pr.merged_date"),
		dal.From("pull_requests pr"),
		dal.Where("pr.merged_date IS NOT NULL AND pr.base_repo_id IN (?)", scope.CandidateRepoIds),
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

			// Which deployment first shipped this PR, on any team webhook, and
			// does it belong to this project?
			deployment := deploymentsMap[pr.MergeCommitSha]
			decision := decidePrEmit(
				deployment != nil,
				deployment != nil && scope.OwnsScope(deployment.CicdScopeId),
				scope.OwnsRepo(pr.BaseRepoId),
			)
			if !decision.Emit {
				// Shipped by another team's webhook (that team emits it), or a
				// never-deployed PR of a repo this project no longer owns.
				return nil, nil
			}
			if decision.Deployed && deployment.FinishedDate != nil {
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

// prEmitDecision says whether the project being calculated writes a
// project_pr_metrics row for a PR, and whether that row carries deploy fields.
type prEmitDecision struct {
	Emit     bool
	Deployed bool
}

// decidePrEmit applies the ownership rule for lead time. A PR shipped by a team
// webhook belongs to that team only; a PR no team webhook has shipped yet stays
// with the project that maps its repo, without deploy fields.
func decidePrEmit(shipped, projectOwnsShipScope, projectOwnsRepo bool) prEmitDecision {
	if shipped {
		return prEmitDecision{Emit: projectOwnsShipScope, Deployed: projectOwnsShipScope}
	}
	return prEmitDecision{Emit: projectOwnsRepo}
}

// normalizeRepoUrl makes a deployment's repo_url comparable with repos.url.
// Webhook deployments carry the clone URL (".git" suffix, sometimes a trailing
// slash, arbitrary case) and an empty repo_id, so this is the only join key.
func normalizeRepoUrl(url string) string {
	u := strings.ToLower(strings.TrimSpace(url))
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.TrimSuffix(u, "/")
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

// batchFetchFirstCommits retrieves the first commit for all pull requests of the given repos.
// Returns a map indexed by PR ID for O(1) lookup performance.
//
// The query uses a subquery to find the minimum commit_authored_date for each PR,
// then joins back to get the full commit record. This is more efficient than
// fetching all commits and filtering in memory.
func batchFetchFirstCommits(repoIds []string, db dal.Dal) (map[string]*code.PullRequestCommit, errors.Error) {
	commitMap := map[string]*code.PullRequestCommit{}
	if len(repoIds) == 0 {
		return commitMap, nil
	}
	var results []*code.PullRequestCommit
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
		dal.Where("pr.base_repo_id IN (?)", repoIds),
		dal.Orderby("prc.pull_request_id, prc.commit_authored_date ASC"),
	)
	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to batch fetch first commits")
	}
	for _, commit := range results {
		// Only keep the first commit if multiple commits have the same timestamp
		if _, exists := commitMap[commit.PullRequestId]; !exists {
			commitMap[commit.PullRequestId] = commit
		}
	}
	return commitMap, nil
}

// batchFetchFirstReviews retrieves the first review comment for all pull requests of the given repos.
// Returns a map indexed by PR ID for O(1) lookup performance.
//
// The query uses a subquery to find the minimum created_date for each PR (excluding the PR author
// and any bot accounts), then joins back to get the full comment record.
func batchFetchFirstReviews(repoIds []string, db dal.Dal, botAccountIds []string) (map[string]*code.PullRequestComment, errors.Error) {
	reviewMap := map[string]*code.PullRequestComment{}
	if len(repoIds) == 0 {
		return reviewMap, nil
	}
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
		dal.Where("pr.base_repo_id IN (?) AND (pr.author_id IS NULL OR pr.author_id = '' OR prc.account_id != pr.author_id)"+botFilterOuter,
			append([]interface{}{repoIds}, outerParams...)...),
		dal.Orderby("prc.pull_request_id, prc.created_date ASC"),
	)

	if err != nil {
		return nil, errors.Default.Wrap(err, "failed to batch fetch first reviews")
	}

	// Build the map for O(1) lookup by PR ID
	for _, review := range results {
		// Only keep the first review if multiple reviews have the same timestamp
		if _, exists := reviewMap[review.PullRequestId]; !exists {
			reviewMap[review.PullRequestId] = review
		}
	}

	return reviewMap, nil
}

// leadTimeScope is what one project's lead-time calculation looks at.
//
// S_P (ScopeIds) is the project's own webhook(s). R_P (OwnedRepoIds) are the
// repos currently mapped to the project. R_ship(P) are repos the project's
// webhook has production-deployed, whether or not they are still mapped to it;
// that is what lets a team keep the PRs it shipped after a repo moves to
// another owner. C_P (CandidateRepoIds) is the union.
type leadTimeScope struct {
	ProjectName         string
	ScopeIds            []string
	OwnedRepoIds        []string
	CandidateRepoIds    []string
	CandidateDeployUrls []string
	MappedScopeIds      []string

	scopeSet map[string]struct{}
	ownedSet map[string]struct{}
}

func (s *leadTimeScope) OwnsScope(scopeId string) bool {
	_, ok := s.scopeSet[scopeId]
	return ok
}

func (s *leadTimeScope) OwnsRepo(repoId string) bool {
	_, ok := s.ownedSet[repoId]
	return ok
}

type projectMappingRow struct {
	ProjectName string `gorm:"column:project_name"`
	RowId       string `gorm:"column:row_id"`
}

type repoUrlRow struct {
	Id  string `gorm:"column:id"`
	Url string `gorm:"column:url"`
}

type deployUrlRow struct {
	RepoUrl string `gorm:"column:repo_url"`
}

// loadLeadTimeScope resolves S_P, R_P, R_ship(P) and C_P for projectName.
// Deployments carry an empty repo_id, so R_ship is resolved by matching
// normalized repo_url against repos.url in Go, which keeps the SQL portable.
func loadLeadTimeScope(projectName string, db dal.Dal, logger log.Logger) (*leadTimeScope, errors.Error) {
	scope := &leadTimeScope{
		ProjectName: projectName,
		scopeSet:    map[string]struct{}{},
		ownedSet:    map[string]struct{}{},
	}

	var scopeRows []*projectMappingRow
	if err := db.All(&scopeRows,
		dal.Select("pm.project_name, pm.row_id"),
		dal.From("project_mapping pm"),
		dal.Where("pm.table = 'cicd_scopes'"),
	); err != nil {
		return nil, errors.Default.Wrap(err, "failed to load cicd scope mappings")
	}
	projectByScope := map[string]string{}
	warnedScopes := map[string]struct{}{}
	for _, row := range scopeRows {
		if first, seen := projectByScope[row.RowId]; !seen {
			projectByScope[row.RowId] = row.ProjectName
		} else if first != row.ProjectName {
			if _, warned := warnedScopes[row.RowId]; !warned {
				warnedScopes[row.RowId] = struct{}{}
				logger.Warn(nil, "cicd scope %s is mapped to more than one project; deployed PRs on it will be counted by each of them", row.RowId)
			}
		}
		scope.MappedScopeIds = append(scope.MappedScopeIds, row.RowId)
		if row.ProjectName == projectName {
			scope.ScopeIds = append(scope.ScopeIds, row.RowId)
			scope.scopeSet[row.RowId] = struct{}{}
		}
	}

	var repoRows []*projectMappingRow
	if err := db.All(&repoRows,
		dal.Select("pm.project_name, pm.row_id"),
		dal.From("project_mapping pm"),
		dal.Where("pm.table = 'repos' AND pm.project_name = ?", projectName),
	); err != nil {
		return nil, errors.Default.Wrap(err, "failed to load repo mappings")
	}
	candidates := map[string]struct{}{}
	for _, row := range repoRows {
		scope.OwnedRepoIds = append(scope.OwnedRepoIds, row.RowId)
		scope.ownedSet[row.RowId] = struct{}{}
		candidates[row.RowId] = struct{}{}
	}

	var repos []*repoUrlRow
	if err := db.All(&repos, dal.Select("id, url"), dal.From("repos")); err != nil {
		return nil, errors.Default.Wrap(err, "failed to load repos")
	}
	reposByUrl := map[string][]string{}
	urlByRepo := map[string]string{}
	for _, r := range repos {
		u := normalizeRepoUrl(r.Url)
		if u == "" {
			continue
		}
		reposByUrl[u] = append(reposByUrl[u], r.Id)
		urlByRepo[r.Id] = u
	}

	// Distinct repo_url values on successful production deployments. ~600 rows;
	// normalized in Go so the match works for ".git" and case differences.
	var deployUrls []*deployUrlRow
	if err := db.All(&deployUrls,
		dal.Select("DISTINCT dc.repo_url"),
		dal.From("cicd_deployment_commits dc"),
		dal.Where("dc.environment = ? AND dc.result = ? AND dc.repo_url IS NOT NULL",
			"PRODUCTION", devops.RESULT_SUCCESS),
	); err != nil {
		return nil, errors.Default.Wrap(err, "failed to load deployment repo urls")
	}

	// R_ship(P): repos deployed on the project's own webhook(s).
	if len(scope.ScopeIds) > 0 {
		var shipped []*deployUrlRow
		if err := db.All(&shipped,
			dal.Select("DISTINCT dc.repo_url"),
			dal.From("cicd_deployment_commits dc"),
			dal.Where("dc.environment = ? AND dc.result = ? AND dc.cicd_scope_id IN (?)",
				"PRODUCTION", devops.RESULT_SUCCESS, scope.ScopeIds),
		); err != nil {
			return nil, errors.Default.Wrap(err, "failed to load repos shipped by project scopes")
		}
		for _, row := range shipped {
			for _, id := range reposByUrl[normalizeRepoUrl(row.RepoUrl)] {
				candidates[id] = struct{}{}
			}
		}
	}

	candidateUrls := map[string]struct{}{}
	for id := range candidates {
		scope.CandidateRepoIds = append(scope.CandidateRepoIds, id)
		if u, ok := urlByRepo[id]; ok {
			candidateUrls[u] = struct{}{}
		}
	}
	sort.Strings(scope.CandidateRepoIds)
	for _, row := range deployUrls {
		if _, ok := candidateUrls[normalizeRepoUrl(row.RepoUrl)]; ok {
			scope.CandidateDeployUrls = append(scope.CandidateDeployUrls, row.RepoUrl)
		}
	}
	return scope, nil
}

// batchFetchDeployments maps each commit of the project's candidate repos to the
// EARLIEST successful production deployment, on ANY mapped webhook, whose commit
// descends from it: the deployment that first shipped that commit. The caller
// looks up a PR by its merge_commit_sha and then decides, with decidePrEmit,
// whether the first-ship deployment belongs to this project.
//
// Loading every mapped webhook's deployments of a repo (not just this project's)
// is what stops an ownership change from back-linking the repo's whole history
// onto the new owner's first deployment: the old owner's deployments already
// claim that history and prune the walk. Deployments on scopes mapped to no
// project (retired webhooks) are ignored; they belong to no team.
//
// Deployments are selected by repo_url or by commit membership in repo_commits
// of the candidate repos, never by which project owns the webhook, so every
// project that considers a repo sees the same deployments and the same commit
// graph for it and agrees on which deployment first shipped a commit. This
// assumes a commit SHA belongs to one repo and a cicd scope is mapped to one
// project; otherwise two projects can still differ.
//
// Requires a complete commit graph (see gitextractor full clone). A repo's very
// first deployment anywhere still claims its pre-tracking history; that case
// has no earlier deployment to bound it and is out of scope here.
func batchFetchDeployments(scope *leadTimeScope, db dal.Dal) (map[string]*devops.CicdDeploymentCommit, errors.Error) {
	if len(scope.MappedScopeIds) == 0 || len(scope.CandidateRepoIds) == 0 {
		return map[string]*devops.CicdDeploymentCommit{}, nil
	}

	// 1. Successful production deployments on mapped webhooks, earliest first,
	//    that match a candidate repo by URL or by commit membership.
	clauses := []dal.Clause{
		dal.Select("dc.*"),
		dal.From("cicd_deployment_commits dc"),
		dal.Where("dc.environment = ?", "PRODUCTION"), // TODO: remove this when multi-environment is supported
		dal.Where("dc.result = ?", devops.RESULT_SUCCESS),
		dal.Where("dc.finished_date IS NOT NULL"),
		dal.Where("dc.cicd_scope_id IN (?)", scope.MappedScopeIds),
	}
	if len(scope.CandidateDeployUrls) > 0 {
		clauses = append(clauses, dal.Where(
			"(dc.repo_url IN (?) OR dc.commit_sha IN (SELECT rc.commit_sha FROM repo_commits rc WHERE rc.repo_id IN (?)))",
			scope.CandidateDeployUrls, scope.CandidateRepoIds))
	} else {
		clauses = append(clauses, dal.Where(
			"dc.commit_sha IN (SELECT rc.commit_sha FROM repo_commits rc WHERE rc.repo_id IN (?))",
			scope.CandidateRepoIds))
	}
	clauses = append(clauses, dal.Orderby("dc.finished_date ASC, dc.id ASC"))
	var deployments []*devops.CicdDeploymentCommit
	if err := db.All(&deployments, clauses...); err != nil {
		return nil, errors.Default.Wrap(err, "failed to fetch deployments")
	}

	// 2. Commit graph of the candidate repos as child -> parents adjacency.
	parents := map[string][]string{}
	var edges []*commitParentEdge
	if err := db.All(&edges,
		dal.Select("cp.commit_sha, cp.parent_commit_sha"),
		dal.From("commit_parents cp"),
		dal.Join("INNER JOIN repo_commits rc ON rc.commit_sha = cp.commit_sha"),
		dal.Where("rc.repo_id IN (?)", scope.CandidateRepoIds),
	); err != nil {
		return nil, errors.Default.Wrap(err, "failed to fetch commit graph")
	}
	for _, e := range edges {
		parents[e.CommitSha] = append(parents[e.CommitSha], e.ParentCommitSha)
	}

	// 3. Attribute each commit to the first deployment that reaches it.
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
