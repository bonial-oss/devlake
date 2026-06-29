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
	"testing"

	"github.com/apache/incubator-devlake/core/models/domainlayer/code"
	"github.com/stretchr/testify/assert"
)

func TestCommitShaSet(t *testing.T) {
	rows := []code.RepoCommit{
		{RepoId: "r1", CommitSha: "aaa"},
		{RepoId: "r1", CommitSha: "bbb"},
		{RepoId: "r1", CommitSha: "aaa"}, // duplicate is harmless
	}
	set := commitShaSet(rows)

	assert.Len(t, set, 2)
	_, hasAAA := set["aaa"]
	_, hasBBB := set["bbb"]
	_, hasCCC := set["ccc"]
	assert.True(t, hasAAA, "already-collected commit should be in the skip set")
	assert.True(t, hasBBB, "already-collected commit should be in the skip set")
	assert.False(t, hasCCC, "uncollected commit must not be skipped")
}

func TestCommitShaSetEmpty(t *testing.T) {
	// A repo with nothing collected yet yields an empty set, so the first run
	// walks every commit (full extraction).
	set := commitShaSet(nil)
	assert.Empty(t, set)
}
