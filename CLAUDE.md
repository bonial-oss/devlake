<!--
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
-->

# Working on this fork

`AGENTS.md` describes the project itself. This file covers how this fork differs
from upstream `apache/devlake`.

## Branches

- `main` is the default branch: upstream plus the fork's own changes. Open every
  pull request against it; unit tests, e2e tests and linters run in its checks.
- Name a branch `release-<topic>` only when the change has to be deployed to a
  staging environment before it is merged. Pushing a `release-*` branch makes
  `Build-Images-Push-GHCR` build and publish images (the longest CI job), which
  is only useful for deploying them.
- That is usually the case for code that changes runtime behaviour: backend
  and plugins, migrations, config-ui, and dashboard SQL whose results need
  checking against real data. It is usually not the case for docs, agent
  instructions, CI configuration, or test-only changes.
- When it is not clear, ask the human whether they plan to test the change on
  staging before picking the branch name. Otherwise use `<type>/<topic>`, e.g.
  `fix/lead-time-rounding`.
- Upstream syncs always use `release-upstream-sync-<yyyymmdd>`, since they are
  validated on staging, and are merged with a merge commit, never squashed, so
  the next sync only brings in new upstream commits.

## Keeping the fork close to upstream

Every change to a file upstream also has is a potential conflict in the next
sync. Prefer fork-only files (like this one) and repository settings over
editing upstream files, and keep changes generic enough to be contributed
upstream.
