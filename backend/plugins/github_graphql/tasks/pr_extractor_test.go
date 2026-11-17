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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConvertGithubPullRequestReadyForReview(t *testing.T) {
	readyAt := time.Date(2026, 3, 2, 10, 30, 0, 0, time.UTC)
	pull := &GraphqlQueryPr{DatabaseId: 1, Number: 7}
	pull.TimelineItems.Nodes = []ReadyForReviewNode{{}}
	pull.TimelineItems.Nodes[0].ReadyForReviewEvent.CreatedAt = readyAt

	githubPull, err := convertGithubPullRequest(pull, 1, 100)
	assert.Nil(t, err)
	if assert.NotNil(t, githubPull.ReadyForReviewAt) {
		assert.True(t, readyAt.Equal(*githubPull.ReadyForReviewAt))
	}
}

func TestConvertGithubPullRequestNeverDraft(t *testing.T) {
	githubPull, err := convertGithubPullRequest(&GraphqlQueryPr{DatabaseId: 1, Number: 7}, 1, 100)
	assert.Nil(t, err)
	assert.Nil(t, githubPull.ReadyForReviewAt)
}

// Raw rows collected before the timeline was requested have no TimelineItems key.
func TestConvertGithubPullRequestOldRawRow(t *testing.T) {
	pull := &GraphqlQueryPr{}
	assert.Nil(t, json.Unmarshal([]byte(`{"DatabaseId":1,"Number":7,"IsDraft":false}`), pull))
	githubPull, err := convertGithubPullRequest(pull, 1, 100)
	assert.Nil(t, err)
	assert.Nil(t, githubPull.ReadyForReviewAt)
}

// The collector stores json.Marshal(GraphqlQueryPr) as raw data; the event must survive it.
func TestReadyForReviewSurvivesRawRoundTrip(t *testing.T) {
	readyAt := time.Date(2026, 3, 2, 10, 30, 0, 0, time.UTC)
	pull := &GraphqlQueryPr{DatabaseId: 1, Number: 7}
	pull.TimelineItems.Nodes = []ReadyForReviewNode{{}}
	pull.TimelineItems.Nodes[0].ReadyForReviewEvent.CreatedAt = readyAt

	raw, jsonErr := json.Marshal(pull)
	assert.Nil(t, jsonErr)
	decoded := &GraphqlQueryPr{}
	assert.Nil(t, json.Unmarshal(raw, decoded))

	githubPull, err := convertGithubPullRequest(decoded, 1, 100)
	assert.Nil(t, err)
	if assert.NotNil(t, githubPull.ReadyForReviewAt) {
		assert.True(t, readyAt.Equal(*githubPull.ReadyForReviewAt))
	}
}
