---
type: how-to
audience: [operator, owner]
owner: ploeg
last_verified: 2026-10-01
verified_by: "Read apps/ploeg pkg/httpapi/deploys.go, pkg/store/{deployments,card}.go, pkg/provider/{forgejo,gitlab}/ancestry.go, cmd/ploegd/operator.go and ops/helm/ploeg/{values.yaml,templates/deployment.yaml}; go test ./pkg/httpapi ./pkg/store ./pkg/provider/... ./pkg/config. Not checked against a live deployment or a live pipeline."
---

# Send deploys from a pipeline to Ploeg

**Goal:** every time a pipeline deploys a repository, it tells Ploeg which commit is now live in which environment. Ploeg then marks the pull requests that commit carries, and their Run cards count days live from the first production deploy instead of from the merge ([ADR-0047](../adrs/0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md)).

Until a repository reports a deploy, its cards count from the merge and say so. Nothing breaks if you never wire this up.

## 1. Give Ploeg a deploy token

The token is a dedicated bearer credential. It can only report deploys. An operator token does not work here.

1. Generate a random value of at least 32 bytes and put it in OpenBao. A person does this, following [Rotate credentials](rotate-credentials.md#steps-shared-by-every-rotation). Git holds only the reference.
2. Sync it into a Kubernetes Secret in the `ploeg` namespace with an ExternalSecret, for example `ploeg-deploy-token` with key `token`.
3. Point the chart at it:

   ```yaml
   deploys:
     tokenSecret:
       name: ploeg-deploy-token
       key: token
   ```

   The chart passes it to ploegd only, as `PLOEG_DEPLOY_TOKEN`. Workers never see it. Without it, `POST /api/v1/deploys` answers 404. A token shorter than 32 bytes stops ploegd at boot.

## 2. Give the pipeline the token and Ploeg's address

Store the same value as a CI secret named `PLOEG_DEPLOY_TOKEN`, and Ploeg's base URL as a variable named `PLOEG_URL`. The pipeline must reach that URL. In the Webgrip estate, a CI secret comes from the Forgejo Actions bridge of the vault; see the [estate secrets model](https://forgejo.webgrip.dev/webgrip/homelab-cluster/src/branch/main/docs/techdocs/docs/adr/adr-0055-one-secrets-model-six-levels.md).

## 3. Add one line after the deploy step

Run it after the deploy succeeded, once per environment. Report the commit that was deployed, not the branch. `--retry` covers a ploegd restart. The trailing `||` keeps a Ploeg outage from failing your deploy; drop it if you want the pipeline to fail instead.

**Forgejo Actions.** Forgejo exposes the GitHub-compatible variables:

```yaml
- name: Send the deploy to Ploeg
  run: >-
    curl -fsS --retry 3 -X POST "${{ vars.PLOEG_URL }}/api/v1/deploys"
    -H "Authorization: Bearer ${{ secrets.PLOEG_DEPLOY_TOKEN }}" -H "Content-Type: application/json"
    -d "{\"environment\":\"production\",\"repo\":{\"forge\":\"forgejo\",\"owner\":\"${GITHUB_REPOSITORY%%/*}\",\"name\":\"${GITHUB_REPOSITORY#*/}\"},\"sha\":\"${GITHUB_SHA}\",\"url\":\"${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_NUMBER}\",\"source\":\"ci\"}"
    || echo "Ploeg did not record the deploy"
```

**GitHub Actions.** The same line, with GitHub's run link. Ploeg has Forgejo and GitLab providers only, so this works for a repository whose pull requests Ploeg opened on one of those, for example a GitHub mirror. `"forge": "github"` answers 422.

```yaml
- name: Send the deploy to Ploeg
  run: >-
    curl -fsS --retry 3 -X POST "${{ vars.PLOEG_URL }}/api/v1/deploys"
    -H "Authorization: Bearer ${{ secrets.PLOEG_DEPLOY_TOKEN }}" -H "Content-Type: application/json"
    -d "{\"environment\":\"production\",\"repo\":{\"forge\":\"forgejo\",\"owner\":\"webgrip\",\"name\":\"unfold\"},\"sha\":\"${GITHUB_SHA}\",\"url\":\"${GITHUB_SERVER_URL}/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}\",\"source\":\"ci\"}"
    || echo "Ploeg did not record the deploy"
```

**GitLab CI.** Mark `PLOEG_DEPLOY_TOKEN` as a masked variable. `CI_PROJECT_NAMESPACE` may contain subgroups; Ploeg accepts them as the owner.

```yaml
send-deploy:
  stage: .post
  needs: [deploy-production]
  script:
    - >-
      curl -fsS --retry 3 -X POST "$PLOEG_URL/api/v1/deploys"
      -H "Authorization: Bearer $PLOEG_DEPLOY_TOKEN" -H "Content-Type: application/json"
      -d "{\"environment\":\"production\",\"repo\":{\"forge\":\"gitlab\",\"owner\":\"$CI_PROJECT_NAMESPACE\",\"name\":\"$CI_PROJECT_NAME\"},\"sha\":\"$CI_COMMIT_SHA\",\"url\":\"$CI_PIPELINE_URL\",\"source\":\"ci\"}"
      || echo "Ploeg did not record the deploy"
```

| Field | Meaning |
| --- | --- |
| `environment` | Free text, lowercased by Ploeg: `development`, `test`, `acceptance`, `production` or your own |
| `repo.forge` | `forgejo`, `gitlab`, or the forge instance id your Work Targets carry |
| `repo.owner`, `repo.name` | The repository as the forge names it |
| `sha` | The full commit hash that is now live |
| `deployedAt` | Optional RFC 3339 time. Default: when Ploeg received it |
| `url` | Optional link to the pipeline run. Ploeg keeps scheme, host and path |
| `source` | Optional `ci`, `gitops` or `manual` |

The [deploy-api.v1 schema](../contracts/deploy-api.v1.schema.json) is the contract.

## 4. Choose the release environment

A card's release is the first deploy to `production`. A Work Target that releases elsewhere names it, on a registered target or on a board's own `repo:` ([Route a board that serves several repositories](route-a-multi-repo-board.md)):

```yaml
targets:
  shop:
    repo: acme/shop
    release:
      environment: live
```

## Verify

1. The pipeline log shows `{"deployId":"…","pullRequests":N}`. `N` counts the pull requests this report marked for the first time; pull requests the sweep marks later are not in it. A repeated call returns the same `deployId` and usually `0`.
2. ploegd logs `deploy recorded` and one `pull request deployed` per marked pull request.
3. In Vloer, the Work Item's card shows the deploy under its environments. Once production has it, the release says "deploy" instead of "counted from merge".

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| 404 `not_found` | `PLOEG_DEPLOY_TOKEN` is not set on ploegd | Set `deploys.tokenSecret` and restart ploegd |
| 401 `unauthorized` | The pipeline sent no token, another token, or an operator token | Check the CI secret matches the vault value |
| 400 `invalid_request` | A field is missing, misspelled or malformed; the message names it | Compare the body with the table above. Unknown fields such as `deployed_at` are refused |
| 422 `unknown_forge` | `repo.forge` names a forge ploegd has no provider for | Use `forgejo`, `gitlab` or the configured instance id |
| `pullRequests` stays 0 and the log says `deploy check failed` | The forge refused the compare call, for example because the bot cannot read the repository | Give the forge token read access; ploegd's sweep retries within the hour |
| Older merged pull requests stay unmarked for a few minutes | One report checks the 50 newest unmarked merges | ploegd's sweep checks 50 more every minute |
| A card shows no release after merging | The repository sends production deploys, but none carried this merge yet | Wait for the next production deploy |
