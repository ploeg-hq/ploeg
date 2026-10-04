---
type: Convention
title: Fake external services with httptest
description: Tests fake forges, LiteLLM and trackers with net/http/httptest; a bug fix lands with a regression test.
resource: https://github.com/ploeg-hq/ploeg/blob/development/AGENTS.md
tags: [test, testing, httptest, forge, litellm]
timestamp: 2026-10-04T00:00:00Z
---

# Fake external services with httptest

Tests fake external services (Forgejo, GitLab, LiteLLM, Vikunja) with
`net/http/httptest`, never with live endpoints. A bug fix lands with a regression
test that fails on the old code.
