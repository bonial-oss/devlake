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

package parser

import (
	"github.com/apache/incubator-devlake/core/dal"
	"github.com/apache/incubator-devlake/core/errors"
	"github.com/apache/incubator-devlake/core/models/domainlayer/code"
)

// commitShaSet turns a slice of repo_commits rows into a lookup set of commit SHAs.
// Kept separate from the DB access so it can be unit-tested without a database.
func commitShaSet(rows []code.RepoCommit) map[string]struct{} {
	shas := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		shas[row.CommitSha] = struct{}{}
	}
	return shas
}

// loadCollectedCommitShas returns the set of commit SHAs already stored for the
// given repo.
//
// Git commits are immutable: once a commit is present in repo_commits it has been
// fully extracted (parents + stats) by a previous run, so re-walking it is pure
// waste. Skipping already-collected commits makes commit collection incremental —
// after the first full extraction only newly fetched commits are walked. This is
// what keeps a full (non-shallow) clone affordable on large repos, where the
// per-commit stat diff dominates the runtime.
func loadCollectedCommitShas(db dal.Dal, repoId string) (map[string]struct{}, errors.Error) {
	var rows []code.RepoCommit
	if err := db.All(&rows, dal.Select("commit_sha"), dal.From("repo_commits"), dal.Where("repo_id = ?", repoId)); err != nil {
		return nil, err
	}
	return commitShaSet(rows), nil
}
