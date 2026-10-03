{{- define "ploeg.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ploeg.labels" -}}
app.kubernetes.io/name: ploeg
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ploeg.selectorLabels" -}}
app.kubernetes.io/name: ploeg
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ploeg.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/*
ploeg.requireIntegrations fails the render when an external service that
ploegd or its workers need is unset.
*/}}
{{- define "ploeg.requireIntegrations" -}}
{{- $e := .Values.executor -}}
{{- $litellm := $e.litellm | default dict -}}
{{- if and (ne $e.workerAuth.mode "legacy") (not $litellm.adminUrl) -}}
{{- fail "executor.litellm.adminUrl is required while executor.workerAuth.mode is managed: ploegd refuses to start without the LiteLLM management URL, e.g. http://litellm.<namespace>.svc.cluster.local:4000" -}}
{{- end -}}
{{- if $e.enabled -}}
{{- if not $litellm.baseUrl -}}
{{- fail "executor.litellm.baseUrl is required when executor.enabled is true: the model gateway URL workers call, e.g. http://litellm.<namespace>.svc.cluster.local:4000/v1" -}}
{{- end -}}
{{- $forge := include "ploeg.forge" . | fromJson -}}
{{- if not $forge.url -}}
{{- fail (printf "executor.%s.url is required when executor.enabled is true: the forge URL workers clone from and push to" $forge.kind) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.apiUrl" -}}
{{ .Values.executor.apiUrl | default (printf "http://%s:%v" (include "ploeg.fullname" .) .Values.service.port) }}
{{- end -}}

{{- define "ploeg.forge" -}}
{{- $e := .Values.executor -}}
{{- $kind := $e.forge | default "forgejo" -}}
{{- $cfg := (index $e $kind) | default dict -}}
{{- dict
      "kind" $kind
      "url" ($cfg.url | default "")
      "tokenSecret" ($cfg.tokenSecret | default dict)
      "readTokenSecret" ($cfg.readTokenSecret | default dict)
    | toJson -}}
{{- end -}}

{{/*
ploeg.teamRoles expands one team into the distinct Roles that need their own
workload, as a JSON array. Both executors range over this, so they cannot
disagree about how many workloads a team has.

A Role recurring across Rounds is ONE workload doing another stint — the
review→fix loop depends on that — so the list is deduplicated by name. Two
definitions of the same name that differ are a configuration error and fail
the render rather than silently picking one: pod shape is fixed at render
time, and a Role cannot be a small model in round 1 and a large one in
round 3.

A team with no plan yields a single empty entry: one workload, no role, and a
pod byte-identical to the pre-Shift shape.
*/}}
{{- define "ploeg.teamRoles" -}}
{{- $roles := list }}
{{- $seen := dict }}
{{- $team := . }}
{{- range $round := ($team.plan | default list) }}
{{- range $role := ($round.roles | default list) }}
{{- $prev := get $seen $role.name }}
{{- if $prev }}
{{- if ne (toJson $prev) (toJson $role) }}
{{- fail (printf "team %s: role %q is defined twice with different settings; one role is one workload, so its shape must not change between rounds" $team.name $role.name) }}
{{- end }}
{{- else }}
{{- $_ := set $seen $role.name $role }}
{{- $roles = append $roles $role }}
{{- end }}
{{- end }}
{{- end }}
{{- if not $roles }}
{{- $roles = list dict }}
{{- end }}
{{- toJson $roles }}
{{- end -}}

{{/*
ploeg.teamMaxRunning is a team's concurrency cap, 0 when unlimited. It
resolves the way ploegd does: config.teams.<name>.maxRunning wins over
executor.teams[].maxRunning.
Context: (dict "root" $ "team" <team>).
*/}}
{{- define "ploeg.teamMaxRunning" -}}
{{- $fromConfig := dig "teams" .team.name "maxRunning" 0 (.root.Values.config | default dict) | int }}
{{- $fromConfig | default (.team.maxRunning | default 0) | int }}
{{- end -}}

{{/*
ploeg.maxReplicaCount is a workload's KEDA ceiling: the role's or team's
maxReplicaCount (default 1), never above the team's concurrency cap.
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.maxReplicaCount" -}}
{{- $replicas := .role.maxReplicaCount | default .team.maxReplicaCount | default 1 | int }}
{{- $cap := include "ploeg.teamMaxRunning" . | int }}
{{- if and (gt $cap 0) (gt $replicas $cap) }}{{ $cap }}{{ else }}{{ $replicas }}{{ end }}
{{- end -}}

{{/*
ploeg.workloadName is the workload's name: <fullname>-worker-<team> for a
plan-less team (unchanged), <fullname>-worker-<team>-<role> for a Role.
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.workloadName" -}}
{{- $name := printf "%s-worker-%s" (include "ploeg.fullname" .root) .team.name }}
{{- if .role.name }}{{- $name = printf "%s-%s" $name .role.name }}{{- end }}
{{- if gt (len $name) 63 }}
{{- fail (printf "workload name %q exceeds 63 characters; shorten the team or role name" $name) }}
{{- end }}
{{- $name }}
{{- end -}}

{{/*
ploeg.workerPodTemplate renders the worker pod template for one team —
shared by every executor (ScaledJob, CronJob). Context: (dict "root" $
"team" <team entry>). The team's optional `harness` block overrides the
global executor.harness defaults field-by-field (explicit hasKey checks, so
`dind: false` overrides correctly — sprig merge would drop it).
*/}}
{{- define "ploeg.workerPodTemplate" -}}
{{- $root := .root }}
{{- $team := .team }}
{{/* role is the (team, role) workload's Role entry; empty dict = a plan-less
     team, whose single workload is byte-identical to the pre-Shift shape. The
     override chain gains a tier: role -> team -> global. */}}
{{- $role := .role | default dict }}
{{- $gh := $root.Values.executor.harness | default dict }}
{{- $th := $team.harness | default dict }}
{{- $rh := $role.harness | default dict }}
{{- $hName := $rh.name | default ($th.name | default ($gh.name | default "openhands")) }}
{{- $hImage := $rh.image | default ($th.image | default ($gh.image | default $root.Values.executor.runnerImage)) }}
{{- if not $hImage }}
{{- fail (printf "team %s: no agent image; set executor.harness.image, or harness.image on the team or Role. Ploeg publishes no agent image" $team.name) }}
{{- end }}
{{- $hEntrypoint := $rh.entrypoint | default ($th.entrypoint | default $gh.entrypoint) }}
{{- $hArgs := $rh.args | default ($th.args | default $gh.args) }}
{{- $hOutcomeFile := $rh.outcomeFile | default ($th.outcomeFile | default $gh.outcomeFile) }}
{{- $hTimeout := $rh.timeout | default ($th.timeout | default $gh.timeout) }}
{{- $hIdleTimeout := $rh.idleTimeout | default ($th.idleTimeout | default $gh.idleTimeout) }}
{{- $hToolchains := $rh.toolchains | default ($th.toolchains | default $gh.toolchains) }}
{{- $hVerify := $rh.verify | default ($th.verify | default $gh.verify) }}
{{- $hVerifyTimeout := $rh.verifyTimeout | default ($th.verifyTimeout | default $gh.verifyTimeout) }}
{{- /* Each toolchain is an image volume the kubelet pulls, mounted read-only
     at /opt/ploeg/toolchains/<name>. The worker learns the mounted PATH
     directories from PLOEG_TOOLCHAINS, so the Run's sandbox needs no
     registry egress (Ploeg ADR-0035). */}}
{{- $toolchainEnv := list }}
{{- range $tc := $hToolchains }}
{{- $mount := printf "/opt/ploeg/toolchains/%s" $tc.name }}
{{- $paths := list }}
{{- range $p := ($tc.path | default (list "/bin")) }}
{{- $paths = append $paths (printf "%s/%s" $mount (trimPrefix "/" $p)) }}
{{- end }}
{{- $toolchainEnv = append $toolchainEnv (dict "name" $tc.name "path" $paths "env" ($tc.env | default dict)) }}
{{- end }}
{{- $hDind := true }}
{{- if hasKey $rh "dind" }}{{- $hDind = $rh.dind }}{{- else if hasKey $th "dind" }}{{- $hDind = $th.dind }}{{- else if hasKey $gh "dind" }}{{- $hDind = $gh.dind }}{{- end }}
{{- $dt := $root.Values.executor.defaultTarget | default dict }}
{{/* acp harness: same field-by-field override, one level deeper. */}}
{{- $ga := $gh.acp | default dict }}
{{- $ta := $th.acp | default dict }}
{{- $ra := $rh.acp | default dict }}
{{- $acpProfile := $ra.profile | default ($ta.profile | default $ga.profile) }}
{{- $acpArgv := $ra.argv | default ($ta.argv | default $ga.argv) }}
{{- $acpPerm := $ra.permissionMode | default ($ta.permissionMode | default $ga.permissionMode) }}
{{- $acpPrompt := $ra.promptTimeout | default ($ta.promptTimeout | default $ga.promptTimeout) }}
{{- $acpIdle := $ra.idleTimeout | default ($ta.idleTimeout | default $ga.idleTimeout) }}
{{- $acpConfig := $ra.configJson | default ($ta.configJson | default $ga.configJson) }}
{{- $who := $team.name }}{{- if $role.name }}{{- $who = printf "%s/%s" $team.name $role.name }}{{- end }}
{{- if and (eq $hName "acp") (eq ($acpProfile | default "opencode") "custom") (not $acpArgv) }}
{{- fail (printf "team %s: harness.acp.profile=custom requires harness.acp.argv" $who) }}
{{- end -}}
metadata:
  labels:
    app.kubernetes.io/name: ploeg-worker
    ploeg.webgrip.dev/team: {{ $team.name }}
    {{- if $role.name }}
    ploeg.webgrip.dev/role: {{ $role.name }}
    {{- end }}
    {{- if $hDind }}
    # Names the HAZARD, not the workload: this pod carries a privileged
    # docker:dind sidecar, and the ploeg-worker PolicyException is keyed on
    # exactly this label. A pod without dind never carries it and is held to
    # the full baseline — so adding a Role costs no security-repo change, and
    # readers are not waived for a privilege they do not take.
    ploeg.webgrip.dev/privileged-dind: "true"
    {{- end }}
spec:
  restartPolicy: Never
  # The shutdown budget for ploeg-worker's SIGTERM path: abort the harness,
  # revoke the per-run credential, settle its spend, report the outcome. The
  # 30s default SIGKILLed the pod mid-report, which is the silence that made a
  # killed run indistinguishable from a hung one and cost the Round an attempt.
  terminationGracePeriodSeconds: {{ $root.Values.executor.terminationGracePeriodSeconds }}
  # An identity of the workers' own. Naming none left them on `default`, which
  # both trips require-non-default-serviceaccount and makes every workload in
  # the namespace indistinguishable in an audit log. The token stays unmounted
  # either way — a worker needs no Kubernetes API authority at all.
  serviceAccountName: {{ include "ploeg.workerServiceAccountName" $root }}
  automountServiceAccountToken: false
  {{- with $root.Values.imagePullSecrets }}
  imagePullSecrets: {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $root.Values.executor.nodeSelector }}
  nodeSelector: {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    fsGroup: 1000
  initContainers:
    # Extracts ploeg-worker from the distroless ploegd image via
    # self-copy (no shell in distroless).
    - name: worker-bin
      image: {{ $root.Values.executor.workerImage | default (include "ploeg.image" $root) }}
      command: ["/usr/local/bin/ploeg-worker", "install", "/mnt/bin/ploeg-worker"]
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
        seccompProfile:
          type: RuntimeDefault
      volumeMounts:
        - name: worker-bin
          mountPath: /mnt/bin
      resources: {{- toYaml $root.Values.executor.workerBinResources | nindent 8 }}
    {{- if $hDind }}
    # DinD sidecar: the harness sandbox and the repo quality gates need a
    # Docker daemon. Requires the ploeg-worker PolicyException. Teams whose
    # harness needs no Docker set harness.dind: false.
    - name: dind
      image: {{ $root.Values.executor.dindImage }}
      restartPolicy: Always
      securityContext:
        privileged: true
      env:
        - name: DOCKER_TLS_CERTDIR
          value: /certs
      startupProbe:
        tcpSocket:
          port: 2376
        periodSeconds: 2
        failureThreshold: 60
        timeoutSeconds: 5
      volumeMounts:
        - name: docker-certs
          mountPath: /certs
        - name: docker-storage
          mountPath: /var/lib/docker
        - name: ci-shared
          mountPath: /mnt/ci-shared
      resources: {{- toYaml (default $root.Values.executor.dindResources $role.dindResources) | nindent 8 }}
    {{- end }}
  containers:
    - name: worker
      image: {{ $hImage }}
      command: ["/mnt/bin/ploeg-worker"]
      env:
        - name: PLOEG_API_URL
          value: {{ include "ploeg.apiUrl" $root | quote }}
        - name: PLOEG_TEAM
          value: {{ $team.name | quote }}
        {{- if $role.name }}
        - name: PLOEG_ROLE
          value: {{ $role.name | quote }}
        {{- end }}
        - name: PLOEG_HARNESS
          value: {{ $hName | quote }}
        {{- if $hEntrypoint }}
        - name: PLOEG_HARNESS_ENTRYPOINT
          value: {{ $hEntrypoint | quote }}
        {{- end }}
        {{- if $hArgs }}
        - name: PLOEG_HARNESS_ARGS
          value: {{ toJson $hArgs | quote }}
        {{- end }}
        {{- if $hOutcomeFile }}
        - name: PLOEG_OUTCOME_FILE
          value: {{ $hOutcomeFile | quote }}
        {{- end }}
        {{- if $hTimeout }}
        - name: PLOEG_HARNESS_TIMEOUT
          value: {{ $hTimeout | quote }}
        {{- end }}
        {{- if $hIdleTimeout }}
        - name: PLOEG_HARNESS_IDLE_TIMEOUT
          value: {{ $hIdleTimeout | quote }}
        {{- end }}
        {{- if $toolchainEnv }}
        - name: PLOEG_TOOLCHAINS
          value: {{ toJson $toolchainEnv | replace "$(" "$$(" | quote }}
        {{- end }}
        {{- if $hVerify }}
        {{- /* $$ keeps Kubernetes from expanding $(VAR) inside a command line. */}}
        - name: PLOEG_VERIFY_COMMANDS
          value: {{ toJson $hVerify | replace "$(" "$$(" | quote }}
        {{- end }}
        {{- if $hVerifyTimeout }}
        - name: PLOEG_VERIFY_TIMEOUT
          value: {{ $hVerifyTimeout | quote }}
        {{- end }}
        {{- if eq $hName "acp" }}
        {{- if $acpProfile }}
        - name: PLOEG_ACP_PROFILE
          value: {{ $acpProfile | quote }}
        {{- end }}
        {{- if $acpArgv }}
        - name: PLOEG_ACP_ARGV
          value: {{ toJson $acpArgv | quote }}
        {{- end }}
        {{- if $acpPerm }}
        - name: PLOEG_ACP_PERMISSION_MODE
          value: {{ $acpPerm | quote }}
        {{- end }}
        {{- if $acpPrompt }}
        - name: PLOEG_ACP_PROMPT_TIMEOUT
          value: {{ $acpPrompt | quote }}
        {{- end }}
        {{- if $acpIdle }}
        - name: PLOEG_ACP_IDLE_TIMEOUT
          value: {{ $acpIdle | quote }}
        {{- end }}
        {{- if $acpConfig }}
        - name: PLOEG_ACP_CONFIG_JSON
          value: {{ $acpConfig | quote }}
        {{- end }}
        {{- end }}
        - name: LLM_MODEL
          value: {{ $role.model | default $team.model | quote }}
        {{- /* For a Role, the team's `budget` is the SHIFT POOL, not a per-run
             ceiling — rendering it here would hand one Run the whole pool if
             this fallback ever applied. A planned Run is always minted at the
             claim's authorization instead (ADR-0012); this value stands only
             for a plan-less team, and for a Role it degrades to its own cap.

             `perRunBudget` exists because `budget` alone meant two different
             things depending on whether a team had a plan: the Shift pool for
             bronze, and the per-run key ceiling for silver and copper, which
             have none. One word, two ceilings, and the HelmRelease comments
             drifted from the values in both directions. Set it to say the
             per-run number out loud; `budget` remains the fallback so no
             existing values file changes meaning. */}}
        - name: LITELLM_KEY_BUDGET
          value: {{ $role.cap | default $team.perRunBudget | default $team.budget | quote }}
        - name: LITELLM_KEY_DURATION
          value: {{ $root.Values.executor.litellm.keyDuration | quote }}
        - name: LLM_BASE_URL
          value: {{ $root.Values.executor.litellm.baseUrl | quote }}
        {{- with $root.Values.executor.litellm.keyIsolation }}
        - name: PLOEG_LLM_KEY_ISOLATION
          value: {{ . | quote }}
        {{- end }}
        {{- with $root.Values.executor.forgeTokenIsolation }}
        - name: PLOEG_FORGE_TOKEN_ISOLATION
          value: {{ . | quote }}
        {{- end }}
        - name: PLOEG_LLM_CREDENTIAL_MODE
          value: {{ ternary "static-compatibility" "managed" (eq $root.Values.executor.workerAuth.mode "legacy") | quote }}
        {{- if eq $root.Values.executor.workerAuth.mode "managed" }}
        - name: PLOEG_WORKER_BOOTSTRAP_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ $root.Values.executor.workerAuth.bootstrapSecret.name }}
              key: {{ printf "%s--%s" $team.name ($role.name | default "default") | quote }}
        {{- else if $root.Values.executor.workerAuth.staticInferenceSecret }}
        - name: LLM_API_KEY
          valueFrom:
            secretKeyRef:
              name: {{ $root.Values.executor.workerAuth.staticInferenceSecret.name }}
              key: {{ $root.Values.executor.workerAuth.staticInferenceSecret.key }}
        {{- end }}
        {{- $forge := include "ploeg.forge" $root | fromJson }}
        - name: PLOEG_TARGET_FORGE
          value: {{ $forge.kind | quote }}
        - name: FORGE_URL
          value: {{ $forge.url | quote }}
        {{- /* ADR-0013 tier 1: a READING Run gets a read-only forge credential,
             so the writer/reader split is enforced by the forge and not only
             by Ploeg's scheduling. The repos are private, so "no credential at
             all" cannot clone — a read-only token is the honest tier 1. A
             reader never falls back to the read-write builder token: a team
             with a reading Role and no readTokenSecret fails the render, and
             PLOEG_FORGE_TOKEN_ACCESS lets the worker refuse a reading claim
             when the pod was built some other way. */}}
        {{- $isReader := and (ne ($role.name | default "") "") (not $role.writes) }}
        {{- $readSecret := $forge.readTokenSecret }}
        {{- if and $isReader (not (and $readSecret.name $readSecret.key)) }}
        {{- fail (printf "team %s: role %q reads but executor.%s.readTokenSecret is not set; a reading Role must not receive the read-write forge token (ADR-0013 tier 1). Set readTokenSecret.name and readTokenSecret.key to a read-only token" $team.name $role.name $forge.kind) }}
        {{- end }}
        {{- if $role.name }}
        - name: PLOEG_FORGE_TOKEN_ACCESS
          value: {{ ternary "read-only" "read-write" $isReader | quote }}
        {{- end }}
        - name: AGENT_BUILDER_TOKEN
          valueFrom:
            secretKeyRef:
              {{- if $isReader }}
              name: {{ $readSecret.name }}
              key: {{ $readSecret.key }}
              {{- else }}
              name: {{ $forge.tokenSecret.name }}
              key: {{ $forge.tokenSecret.key }}
              {{- end }}
        # FALLBACK target only. The repository belongs to the work item,
        # resolved at ingest from its tracker scope and delivered on the claim
        # (R11, ADR-0001); these render only while a team still pins a repo,
        # and a team that declares none renders no repo env at all.
        {{- $repoOwner := $team.repoOwner | default $dt.owner }}
        {{- $repoName := $team.repoName | default $dt.name }}
        {{- $baseBranch := $team.baseBranch | default $dt.baseBranch }}
        {{- if $repoOwner }}
        - name: REPO_OWNER
          value: {{ $repoOwner | quote }}
        {{- end }}
        {{- if $repoName }}
        - name: REPO_NAME
          value: {{ $repoName | quote }}
        {{- end }}
        {{- if $baseBranch }}
        # Base branch for clone/branch/PR (VIK-589). Unset = the repo's
        # default branch on clone and the worker's historical "main".
        - name: PLOEG_BASE_BRANCH
          value: {{ $baseBranch | quote }}
        {{- end }}
        {{- if $team.targetSource }}
        # env = ignore the claim's target and use this team's pinned repo.
        # The per-team lever for rolling the decoupling forward or back.
        - name: PLOEG_TARGET_SOURCE
          value: {{ $team.targetSource | quote }}
        {{- end }}
        # Downward API: node+pod identity survives pod/job cleanup for
        # run forensics (VIK-597). Logged at worker start and embedded in
        # every checkpoint.
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: POD_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
        - name: WORK_DIR
          value: /mnt/ci-shared
        {{- if $hDind }}
        - name: DOCKER_HOST
          value: tcp://localhost:2376
        - name: DOCKER_CERT_PATH
          value: /certs/client
        - name: DOCKER_TLS_VERIFY
          value: "1"
        {{- end }}
      volumeMounts:
        - name: worker-bin
          mountPath: /mnt/bin
          readOnly: true
        {{- if $hDind }}
        - name: docker-certs
          mountPath: /certs
          readOnly: true
        {{- end }}
        - name: ci-shared
          mountPath: /mnt/ci-shared
        {{- range $tc := $hToolchains }}
        - name: toolchain-{{ $tc.name }}
          mountPath: /opt/ploeg/toolchains/{{ $tc.name }}
          readOnly: true
        {{- end }}
      {{- /* Per-Role sizing, falling back to the executor-wide default. A
           Round that fans out three readers asks the scheduler for three
           whole writer-sized pods at once, which on a one-node worker pool
           simply does not fit — and the readers do not build anything, so
           they never needed a builder's CPU. Same field-by-field override
           shape as harness above. */}}
      resources: {{- toYaml (default $root.Values.executor.workerResources $role.workerResources) | nindent 8 }}
  volumes:
    - name: worker-bin
      emptyDir: {}
    {{- if $hDind }}
    - name: docker-certs
      emptyDir: {}
    - name: docker-storage
      emptyDir: {}
    {{- end }}
    # Disk-backed (not tmpfs): repo checkout + gate builds are large.
    # Same absolute path in worker AND dind so `docker run -v`
    # bind-mounts resolve on the daemon's filesystem.
    - name: ci-shared
      emptyDir:
        sizeLimit: 8Gi
    {{- range $tc := $hToolchains }}
    - name: toolchain-{{ $tc.name }}
      image:
        reference: {{ $tc.image }}
        pullPolicy: IfNotPresent
    {{- end }}
{{- end -}}

{{/*
ploeg.workerServiceAccountName is the identity the worker pods run as.

One helper so the object and the reference cannot disagree. They did: the
create guard used to be `not .Values.executor.serviceAccountName`, reading a
name as "an external account exists", so naming the chart's own default
suppressed the account the pods then referenced and every Job died at
admission with `serviceaccount "ploeg-worker" not found`. create and name are
separate questions and are now separate keys.
*/}}
{{- define "ploeg.workerServiceAccountName" -}}
{{- .Values.executor.serviceAccount.name | default (printf "%s-worker" (include "ploeg.fullname" .)) -}}
{{- end -}}

{{/*
ploeg.roleUsesDind resolves the dind flag for one (team, role) through the
role -> team -> global override chain. Context: (dict "root" $ "team" <team>
"role" <role>).

Extracted so the ScaledJob and the pod template cannot disagree. They did, and
it broke production: the hazard label was stamped only on the pod TEMPLATE,
but Kyverno autogens a Job rule for pod-security-baseline-enforce and a Job
selector matches the JOB's own labels — so every DinD team was denied at
admission while the PolicyException looked correct.
*/}}
{{- define "ploeg.roleUsesDind" -}}
{{- $gh := .root.Values.executor.harness | default dict }}
{{- $th := .team.harness | default dict }}
{{- $rh := (.role | default dict).harness | default dict }}
{{- $d := true }}
{{- if hasKey $rh "dind" }}{{- $d = $rh.dind }}{{- else if hasKey $th "dind" }}{{- $d = $th.dind }}{{- else if hasKey $gh "dind" }}{{- $d = $gh.dind }}{{- end }}
{{- if $d }}true{{- end }}
{{- end -}}
