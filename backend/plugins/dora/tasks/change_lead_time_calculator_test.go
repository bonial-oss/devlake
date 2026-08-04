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
	"regexp"
	"testing"

	"github.com/apache/incubator-devlake/core/models/domainlayer"
	"github.com/apache/incubator-devlake/core/models/domainlayer/crossdomain"
	"github.com/apache/incubator-devlake/core/models/domainlayer/devops"
	"github.com/stretchr/testify/assert"
)

func account(id, userName string) *crossdomain.Account {
	return &crossdomain.Account{DomainEntity: domainlayer.DomainEntity{Id: id}, UserName: userName}
}

// The same pattern bonial runs in production for the PR-author bot filter.
var botPattern = regexp.MustCompile(`.*(\[bot\]|-bot).*`)

func TestBotAccountIdSet(t *testing.T) {
	accounts := []*crossdomain.Account{
		account("acc:1", "github-actions[bot]"),
		account("acc:2", "jane-doe"),
		account("acc:3", "bonial-renovate-bot"),
		account("acc:4", "john"),
	}
	ids := botAccountIdSet(accounts, botPattern)
	assert.Equal(t, []string{"acc:1", "acc:3"}, ids, "only bot-matching accounts, sorted")
}

func TestBotAccountIdSetNilRegex(t *testing.T) {
	// Bot filtering disabled: no accounts are excluded, first-review behavior is unchanged.
	accounts := []*crossdomain.Account{account("acc:1", "github-actions[bot]")}
	assert.Nil(t, botAccountIdSet(accounts, nil))
}

func TestBotAccountIdSetNoMatches(t *testing.T) {
	accounts := []*crossdomain.Account{account("acc:1", "jane-doe"), account("acc:2", "abbot")}
	assert.Empty(t, botAccountIdSet(accounts, botPattern), "abbot has no '-bot' or '[bot]' token and must not match")
}

func deploy(id, commitSha string) *devops.CicdDeploymentCommit {
	return &devops.CicdDeploymentCommit{CicdDeploymentId: id, CommitSha: commitSha}
}

// ownerOf returns the deployment id attributed to a commit (or "" if unattributed).
func ownerOf(m map[string]*devops.CicdDeploymentCommit, sha string) string {
	if d, ok := m[sha]; ok {
		return d.CicdDeploymentId
	}
	return ""
}

// Linear history c1 <- c2 <- c3, deployed by d1 at the tip: every commit belongs to d1.
func TestAttributeCommits_LinearHistory(t *testing.T) {
	parents := map[string][]string{
		"c3": {"c2"},
		"c2": {"c1"},
	}
	deployments := []*devops.CicdDeploymentCommit{deploy("d1", "c3")}

	m := attributeCommitsToDeployments(deployments, parents)

	assert.Equal(t, "d1", ownerOf(m, "c3"))
	assert.Equal(t, "d1", ownerOf(m, "c2"))
	assert.Equal(t, "d1", ownerOf(m, "c1"))
}

// Two deployments on the same line: the older one (d1, listed first) owns the shared
// ancestors; the newer one (d2) owns only what d1 didn't already claim. This is the core
// "first deployment that shipped it" rule and the prune-at-claimed behavior.
func TestAttributeCommits_EarliestDeploymentWins(t *testing.T) {
	// c1 <- c2 <- c3 <- c4 ; d1 shipped c2, d2 later shipped c4.
	parents := map[string][]string{
		"c4": {"c3"},
		"c3": {"c2"},
		"c2": {"c1"},
	}
	deployments := []*devops.CicdDeploymentCommit{
		deploy("d1", "c2"), // oldest first
		deploy("d2", "c4"),
	}

	m := attributeCommitsToDeployments(deployments, parents)

	assert.Equal(t, "d1", ownerOf(m, "c1"), "shared ancestor belongs to the earliest deployment")
	assert.Equal(t, "d1", ownerOf(m, "c2"))
	assert.Equal(t, "d2", ownerOf(m, "c3"), "commits after d1's tip belong to d2")
	assert.Equal(t, "d2", ownerOf(m, "c4"))
}

// A merge commit has two parents; both ancestry lines must be attributed to the deployment.
func TestAttributeCommits_MergeCommitFollowsBothParents(t *testing.T) {
	// base <- featA, base <- featB, merge has parents featA and featB.
	parents := map[string][]string{
		"merge": {"featA", "featB"},
		"featA": {"base"},
		"featB": {"base"},
	}
	deployments := []*devops.CicdDeploymentCommit{deploy("d1", "merge")}

	m := attributeCommitsToDeployments(deployments, parents)

	assert.Equal(t, "d1", ownerOf(m, "merge"))
	assert.Equal(t, "d1", ownerOf(m, "featA"))
	assert.Equal(t, "d1", ownerOf(m, "featB"))
	assert.Equal(t, "d1", ownerOf(m, "base"))
}

// Deployments with an empty commit sha (e.g. unresolved) are skipped, not attributed.
func TestAttributeCommits_EmptyCommitShaSkipped(t *testing.T) {
	parents := map[string][]string{"c2": {"c1"}}
	deployments := []*devops.CicdDeploymentCommit{
		deploy("d0", ""), // skipped
		deploy("d1", "c2"),
	}

	m := attributeCommitsToDeployments(deployments, parents)

	assert.Len(t, m, 2)
	assert.Equal(t, "d1", ownerOf(m, "c2"))
	assert.Equal(t, "d1", ownerOf(m, "c1"))
}

// A deployment commit absent from the parents graph (no known ancestors) still claims
// itself. Guards against the walk silently dropping tips with a truncated graph.
func TestAttributeCommits_TipNotInGraphClaimsItself(t *testing.T) {
	m := attributeCommitsToDeployments(
		[]*devops.CicdDeploymentCommit{deploy("d1", "loneCommit")},
		map[string][]string{},
	)

	assert.Equal(t, "d1", ownerOf(m, "loneCommit"))
	assert.Len(t, m, 1)
}

func TestAttributeCommits_NoDeployments(t *testing.T) {
	m := attributeCommitsToDeployments(nil, map[string][]string{"c2": {"c1"}})
	assert.Empty(t, m)
}
