{{/*
ploeg.teamExecutorType is the executor one team's workloads run under: the
team's executorType, else executor.type. A team may choose keda or sandbox only
while the deployment's executor.type is one of those two.
Context: (dict "root" $ "team" <team>).
*/}}
{{- define "ploeg.teamExecutorType" -}}
{{- $global := .root.Values.executor.type | default "keda" }}
{{- if and .team.executorType (not (has $global (list "keda" "sandbox"))) }}
{{- fail (printf "team %s: executorType is only honoured when executor.type is keda or sandbox, not %s" .team.name $global) }}
{{- end }}
{{- .team.executorType | default $global }}
{{- end -}}

{{/*
ploeg.anyTeamOnSandbox is "true" when at least one team runs under the
sandbox executor. Context: the root context.
*/}}
{{- define "ploeg.anyTeamOnSandbox" -}}
{{- $root := . }}
{{- $any := false }}
{{- range $team := .Values.executor.teams }}
{{- if eq (include "ploeg.teamExecutorType" (dict "root" $root "team" $team)) "sandbox" }}{{- $any = true }}{{- end }}
{{- end }}
{{- if $any }}true{{- end }}
{{- end -}}

{{/*
ploeg.runtimeClassName resolves the RuntimeClass for one (team, role)
SandboxTemplate through the role -> team -> global chain, the same
field-by-field override shape as harness: a Role's sandbox.runtimeClassName
wins over its team's, which wins over executor.sandbox.runtimeClassName. Empty
at every tier yields "", and sandbox.yaml then renders no runtimeClassName at
all (the node's default runtime).
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.runtimeClassName" -}}
{{- $rh := (.role | default dict).sandbox | default dict }}
{{- $th := .team.sandbox | default dict }}
{{- $gh := .root.Values.executor.sandbox | default dict }}
{{- $rh.runtimeClassName | default $th.runtimeClassName | default $gh.runtimeClassName | default "" }}
{{- end -}}

{{- define "ploeg.sandboxLauncherName" -}}
{{- printf "%s-sandbox-launcher" (include "ploeg.fullname" .) -}}
{{- end -}}

{{/*
ploeg.sandboxLauncherPodTemplate is the ScaledJob pod for executor.type
sandbox: it creates one SandboxClaim for its Job and waits for it. It holds the
only Kubernetes API token in the executor, scoped by its Role to sandboxclaims.
Context: (dict "root" $ "team" <team> "role" <role>).
*/}}
{{- define "ploeg.sandboxLauncherPodTemplate" -}}
{{- $root := .root }}
{{- $sb := $root.Values.executor.sandbox }}
metadata:
  labels:
    app.kubernetes.io/name: ploeg-sandbox-launcher
    ploeg.webgrip.dev/team: {{ .team.name }}
    {{- if .role.name }}
    ploeg.webgrip.dev/role: {{ .role.name }}
    {{- end }}
spec:
  restartPolicy: Never
  serviceAccountName: {{ include "ploeg.sandboxLauncherName" $root }}
  automountServiceAccountToken: true
  {{- with $root.Values.imagePullSecrets }}
  imagePullSecrets: {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $root.Values.executor.nodeSelector }}
  nodeSelector: {{- toYaml . | nindent 4 }}
  {{- end }}
  containers:
    - name: launcher
      image: {{ $root.Values.executor.workerImage | default (include "ploeg.image" $root) }}
      command: ["/usr/local/bin/ploeg-worker", "sandbox-launch"]
      env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: PLOEG_SANDBOX_JOB_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.labels['batch.kubernetes.io/job-name']
        - name: PLOEG_SANDBOX_JOB_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.labels['batch.kubernetes.io/controller-uid']
        - name: PLOEG_SANDBOX_WARM_POOL
          value: {{ include "ploeg.workloadName" . | quote }}
        - name: PLOEG_SANDBOX_RUN_DEADLINE
          value: {{ printf "%vs" $root.Values.executor.activeDeadlineSeconds | quote }}
        - name: PLOEG_SANDBOX_SHUTDOWN_MARGIN
          value: {{ printf "%vs" $sb.shutdownMarginSeconds | quote }}
        - name: PLOEG_SANDBOX_START_TIMEOUT
          value: {{ printf "%vs" $sb.startTimeoutSeconds | quote }}
        - name: PLOEG_SANDBOX_TTL_SECONDS_AFTER_FINISHED
          value: {{ $sb.ttlSecondsAfterFinished | quote }}
        {{- /* A sandbox that never starts fails one Run as infra_node, so
             the launcher claims it as its Team and Role like a worker. */}}
        - name: PLOEG_API_URL
          value: {{ include "ploeg.apiUrl" $root | quote }}
        - name: PLOEG_TEAM
          value: {{ .team.name | quote }}
        {{- if .role.name }}
        - name: PLOEG_ROLE
          value: {{ .role.name | quote }}
        {{- end }}
        - name: PLOEG_WORKER_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
        {{- if eq $root.Values.executor.workerAuth.mode "managed" }}
        - name: PLOEG_WORKER_BOOTSTRAP_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ $root.Values.executor.workerAuth.bootstrapSecret.name }}
              key: {{ printf "%s--%s" .team.name (.role.name | default "default") | quote }}
        {{- end }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: [ALL]
        seccompProfile:
          type: RuntimeDefault
      resources: {{- toYaml $sb.launcherResources | nindent 8 }}
{{- end -}}
