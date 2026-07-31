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
	"github.com/apache/incubator-devlake/plugins/github/models"
)

type GithubAccountEdge struct {
	Login     string
	Id        int `graphql:"databaseId"`
	Name      string
	Company   string
	Email     string
	AvatarUrl string
	HtmlUrl   string `graphql:"url"`
}

// GithubBotAccountEdge is the Bot-type counterpart of GithubAccountEdge; Bot has no name/company/email.
type GithubBotAccountEdge struct {
	Login string
	Id    int `graphql:"databaseId"`
}

// GraphqlInlineAccountQuery is for Actor-typed fields (author, mergedBy, review author). The Bot
// fragment recovers login/id for GitHub App actors, which the User fragment alone returns empty.
type GraphqlInlineAccountQuery struct {
	GithubAccountEdge `graphql:"... on User"`
	Bot               GithubBotAccountEdge `graphql:"... on Bot"`
}

// GraphqlInlineUserQuery is for concrete User-typed fields (assignees, commit author user), which
// reject a Bot fragment.
type GraphqlInlineUserQuery struct {
	GithubAccountEdge `graphql:"... on User"`
}

func appendGraphqlPreAccount(result *[]interface{}, edge *GithubAccountEdge, repoId int, connId uint64) {
	if edge == nil || edge.Id == 0 {
		return
	}
	*result = append(*result, &models.GithubRepoAccount{
		ConnectionId: connId,
		RepoGithubId: repoId,
		Login:        edge.Login,
		AccountId:    edge.Id,
	})
}

func extractGraphqlPreAccount(result *[]interface{}, res *GraphqlInlineAccountQuery, repoId int, connId uint64) {
	if res == nil {
		return
	}
	appendGraphqlPreAccount(result, &res.GithubAccountEdge, repoId, connId)
}

func extractGraphqlPreUserAccount(result *[]interface{}, res *GraphqlInlineUserQuery, repoId int, connId uint64) {
	if res == nil {
		return
	}
	appendGraphqlPreAccount(result, &res.GithubAccountEdge, repoId, connId)
}
