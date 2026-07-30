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

	"github.com/merico-ai/graphql"
	"github.com/stretchr/testify/assert"
)

// Actor fields carry the Bot fragment; concrete User fields must not (GitHub rejects it).
func TestPrQueryFragmentsMatchGithubTypes(t *testing.T) {
	query, _ := graphql.ConstructQuery(&GraphqlQueryPrWrapper{}, map[string]interface{}{
		"pageSize":   graphql.Int(10),
		"skipCursor": (*graphql.String)(nil),
		"owner":      graphql.String("o"),
		"name":       graphql.String("n"),
	})

	// Actor fields: both User and Bot fragments.
	assert.Contains(t, query, "author{... on User{login,databaseId,name,company,email,avatarUrl,url},... on Bot{login,databaseId}}")
	assert.Contains(t, query, "mergedBy{... on User{login,databaseId,name,company,email,avatarUrl,url},... on Bot{login,databaseId}}")

	// Concrete User fields: User fragment only, never a Bot fragment (GitHub would reject it).
	assert.Contains(t, query, "user{... on User{login,databaseId,name,company,email,avatarUrl,url}}")
	assert.NotContains(t, query, "user{... on User{login,databaseId,name,company,email,avatarUrl,url},... on Bot")
	assert.Contains(t, query, "assignees(first: 1){nodes{... on User{login,databaseId,name,company,email,avatarUrl,url}}}")
}

// A bot actor (login/id, no user-specific fields) must still reach AuthorName/AuthorId in the tool layer.
func TestBotAuthorDecodesToToolLayer(t *testing.T) {
	raw := `{
		"DatabaseId": 1000001,
		"Number": 16,
		"State": "MERGED",
		"Title": "chore(deps): update dependency",
		"Author": {"Login": "renovate-bot", "Id": 12345},
		"MergedBy": {"Login": "renovate-bot", "Id": 12345}
	}`

	pr := &GraphqlQueryPr{}
	assert.NoError(t, json.Unmarshal([]byte(raw), pr))

	githubPr, err := convertGithubPullRequest(pr, 1, 42)
	assert.Nil(t, err)
	assert.Equal(t, "renovate-bot", githubPr.AuthorName)
	assert.Equal(t, 12345, githubPr.AuthorId)
	assert.Equal(t, "renovate-bot", githubPr.MergedByName)
	assert.Equal(t, 12345, githubPr.MergedById)
}
