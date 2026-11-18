{{- define "ploeg.claudeRunnerOrchestrator" -}}
{{- $r := .Values.claudeRunner -}}
{{- $mode := $r.mode | default "fleet" -}}
{{- if not (has $mode (list "fleet" "orchestrator")) -}}
{{- fail (printf "claudeRunner.mode must be fleet or orchestrator, not %q" $mode) -}}
{{- end -}}
{{- if and $r.enabled (eq $mode "orchestrator") -}}true{{- end -}}
{{- end -}}

{{- define "ploeg.claudeRunnerOrchestratorName" -}}
{{- printf "%s-orchestrator" (include "ploeg.claudeRunnerName" . | trunc 50 | trimSuffix "-") -}}
{{- end -}}

{{- define "ploeg.claudeRunnerSessionName" -}}
{{- printf "%s-session" (include "ploeg.claudeRunnerName" . | trunc 55 | trimSuffix "-") -}}
{{- end -}}

{{- define "ploeg.claudeRunnerSessionSelectorLabels" -}}
app.kubernetes.io/name: ploeg-claude-runner-session
{{- end -}}

{{- define "ploeg.claudeRunnerSessionUrl" -}}
{{- if include "ploeg.credentialGatewayEnabled" . -}}
{{- printf "http://%s.%s.svc:8084" (include "ploeg.credentialGatewayName" .) (include "ploeg.executorNamespace" .) -}}
{{- else -}}
{{- .Values.claudeRunner.ploegUrl | default (include "ploeg.workerApiUrl" .) -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.claudeCodeEnv" -}}
{{- $cc := .Values.claudeRunner.claudeCode | default dict -}}
{{- $env := list -}}
{{- with $cc.version }}{{- $env = append $env (dict "name" "CLAUDE_CODE_VERSION" "value" .) }}{{- end -}}
{{- with $cc.sha256 }}{{- $env = append $env (dict "name" "CLAUDE_CODE_SHA256" "value" .) }}{{- end -}}
{{- toJson $env -}}
{{- end -}}

{{- define "ploeg.claudeCodeInstallContainer" -}}
{{- $r := .Values.claudeRunner -}}
{{- $env := list (dict "name" "PLOEG_CLAUDE_CODE_DIR" "value" "/opt/claude-code") -}}
{{- $env = concat $env (include "ploeg.claudeCodeEnv" . | fromJsonArray) -}}
{{- range $key, $value := $r.env -}}
{{- if and (hasPrefix "PLOEG_CLAUDE_CODE_" $key) (ne $key "PLOEG_CLAUDE_CODE_DIR") -}}
{{- $env = append $env (dict "name" $key "value" ($value | toString)) -}}
{{- end -}}
{{- end -}}
name: claude-code
image: {{ include "ploeg.claudeRunnerImage" . }}
command: ["ploeg-install-claude-code"]
env: {{- toYaml $env | nindent 2 }}
securityContext:
  runAsUser: 65532
  runAsGroup: 1000
  allowPrivilegeEscalation: false
  capabilities:
    drop: [ALL]
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    memory: 512Mi
volumeMounts:
  - name: claude-code
    mountPath: /opt/claude-code
{{- end -}}

{{- define "ploeg.claudeRunnerJob" -}}
{{- $root := . -}}
{{- $r := .Values.claudeRunner -}}
{{- $o := $r.orchestrator | default dict -}}
{{- $cc := $r.claudeCode | default dict -}}
{{- $gateway := include "ploeg.credentialGatewayEnabled" . -}}
{{- $nodeSelector := $r.nodeSelector | default .Values.nodeSelector -}}
{{- if $gateway }}{{- $nodeSelector = .Values.executor.nodeSelector }}{{- end -}}
apiVersion: batch/v1
kind: Job
metadata:
  name: ""
  namespace: {{ include "ploeg.executorNamespace" . }}
  labels:
    {{- include "ploeg.claudeRunnerSessionSelectorLabels" . | nindent 4 }}
    {{- include "ploeg.componentLabels" (dict "root" .) | nindent 4 }}
    ploeg.webgrip.dev/component: claude-runner-session
spec:
  backoffLimit: 0
  activeDeadlineSeconds: {{ $o.runnerActiveDeadlineSeconds }}
  ttlSecondsAfterFinished: {{ $o.runnerTtlSecondsAfterFinished }}
  template:
    metadata:
      labels:
        {{- include "ploeg.claudeRunnerSessionSelectorLabels" . | nindent 8 }}
        app.kubernetes.io/part-of: ploeg
        ploeg.webgrip.dev/component: claude-runner-session
        {{- if and $gateway .Values.executor.credentialGateway.acknowledgeSingleNode }}
        ploeg.webgrip.dev/credential-gateway-shared-node: "true"
        {{- end }}
      annotations:
        {{- with $r.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        ploeg.webgrip.dev/claude-session: ""
    spec:
      restartPolicy: Never
      serviceAccountName: {{ include "ploeg.claudeRunnerSessionName" . }}
      automountServiceAccountToken: false
      enableServiceLinks: false
      {{- with $r.runtimeClassName }}
      runtimeClassName: {{ . }}
      {{- end }}
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets: {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $nodeSelector }}
      nodeSelector: {{- toYaml . | nindent 8 }}
      {{- end }}
      terminationGracePeriodSeconds: {{ $r.terminationGracePeriodSeconds }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      {{- if $cc.install }}
      initContainers:
        - {{- include "ploeg.claudeCodeInstallContainer" . | nindent 10 }}
      {{- end }}
      containers:
        - name: runner
          image: {{ include "ploeg.claudeRunnerImage" . }}
          command: ["ploeg-claude-runner-from-work-order"]
          args:
            - --base-dir
            - /home/runner/work
            - --hooks-dir
            - /opt/ploeg/claude-runner/hooks
            {{- range (concat ($r.args | default list) ($r.extraArgs | default list)) }}
            - {{ . | quote }}
            {{- end }}
          env:
            - name: PLOEG_URL
              value: {{ include "ploeg.claudeRunnerSessionUrl" . | quote }}
            {{- with (include "ploeg.claudeCodeEnv" . | fromJsonArray) }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- range $key, $value := $r.env }}
            - name: {{ $key }}
              value: {{ $value | quote }}
            {{- end }}
            {{- with $r.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          ports:
            - name: health
              containerPort: 8080
          readinessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 5
            periodSeconds: 10
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
          {{- with $r.resources }}
          resources: {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: work
              mountPath: /home/runner/work
            - name: work-order
              mountPath: /var/run/ploeg/work-order
            {{- if $cc.install }}
            - name: claude-code
              mountPath: /opt/claude-code
              readOnly: true
            {{- end }}
      volumes:
        - name: work
          emptyDir:
            sizeLimit: {{ $r.workSizeLimit }}
        - name: work-order
          emptyDir:
            medium: Memory
            sizeLimit: 1Mi
        {{- if $cc.install }}
        - name: claude-code
          emptyDir:
            sizeLimit: 1Gi
        {{- end }}
{{- end -}}

{{- define "ploeg.runPodSelectors" -}}
{{- $selectors := list (dict "app.kubernetes.io/name" "ploeg-worker") -}}
{{- if include "ploeg.claudeRunnerOrchestrator" . -}}
{{- $selectors = append $selectors (include "ploeg.claudeRunnerSessionSelectorLabels" . | fromYaml) -}}
{{- end -}}
{{- toJson $selectors -}}
{{- end -}}

{{- define "ploeg.offRunNodesAffinity" -}}
{{- $root := .root -}}
{{- $gw := $root.Values.executor.credentialGateway -}}
{{- $runPods := list -}}
{{- range (include "ploeg.runPodSelectors" $root | fromJsonArray) -}}
{{- $runPods = append $runPods (dict "labelSelector" (dict "matchLabels" .) "topologyKey" "kubernetes.io/hostname") -}}
{{- end -}}
{{- $replicas := dict "labelSelector" (dict "matchLabels" .self) "topologyKey" "kubernetes.io/hostname" -}}
{{- $antiAffinity := dict -}}
{{- $preferred := list -}}
{{- if $gw.acknowledgeSingleNode -}}
{{- range $runPods -}}
{{- $preferred = append $preferred (dict "weight" 100 "podAffinityTerm" .) -}}
{{- end -}}
{{- else -}}
{{- $_ := set $antiAffinity "requiredDuringSchedulingIgnoredDuringExecution" $runPods -}}
{{- end -}}
{{- $preferred = append $preferred (dict "weight" 100 "podAffinityTerm" $replicas) -}}
{{- $_ := set $antiAffinity "preferredDuringSchedulingIgnoredDuringExecution" $preferred -}}
{{- $affinity := dict "podAntiAffinity" $antiAffinity -}}
{{- with $root.Values.executor.nodeSelector -}}
{{- $terms := list -}}
{{- range $key, $value := . -}}
{{- $terms = append $terms (dict "matchExpressions" (list (dict "key" $key "operator" "NotIn" "values" (list $value)))) -}}
{{- end -}}
{{- if $gw.acknowledgeSingleNode -}}
{{- $weighted := list -}}
{{- range $term := $terms -}}
{{- $weighted = append $weighted (dict "weight" 100 "preference" $term) -}}
{{- end -}}
{{- $_ := set $affinity "nodeAffinity" (dict "preferredDuringSchedulingIgnoredDuringExecution" $weighted) -}}
{{- else -}}
{{- $_ := set $affinity "nodeAffinity" (dict "requiredDuringSchedulingIgnoredDuringExecution" (dict "nodeSelectorTerms" $terms)) -}}
{{- end -}}
{{- end -}}
{{- toYaml $affinity -}}
{{- end -}}

{{- define "ploeg.claudeRunnerForbiddenEnv" -}}
{{- list "CLAUDE_CODE_OAUTH_TOKEN" "CLAUDE_CODE_OAUTH_REFRESH_TOKEN" "ANTHROPIC_API_KEY" "ANTHROPIC_AUTH_TOKEN" "SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET" "CLAUDE_CONFIG_DIR" | toJson -}}
{{- end -}}

{{- define "ploeg.claudeRunnerOrchestratorObjects" -}}
{{- $r := .Values.claudeRunner -}}
{{- $o := $r.orchestrator | default dict -}}
{{- $cc := $r.claudeCode | default dict -}}
{{- $name := include "ploeg.claudeRunnerOrchestratorName" . -}}
{{- $session := include "ploeg.claudeRunnerSessionName" . -}}
{{- $selector := dict "app.kubernetes.io/name" $name -}}
{{- $job := include "ploeg.claudeRunnerJob" . | fromYaml -}}
{{- if include "ploeg.credentialGatewayEnabled" . -}}
{{- include "ploeg.requireCredentialFreeRunPod" (dict "pod" $job.spec.template "who" "claudeRunner session") -}}
{{- range $c := $job.spec.template.spec.containers -}}
{{- range $e := ($c.env | default list) -}}
{{- if has $e.name (include "ploeg.claudeRunnerForbiddenEnv" $ | fromJsonArray) -}}
{{- fail (printf "claudeRunner renders %s into the runner pod, where a session runs; with executor.credentialGateway.enabled no Claude login, Anthropic key or environment secret enters it (ADR-0197 section 7)" $e.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $session }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
automountServiceAccountToken: false
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $name }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
automountServiceAccountToken: true
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ $name }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ $name }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ $name }}
subjects:
  - kind: ServiceAccount
    name: {{ $name }}
    namespace: {{ include "ploeg.executorNamespace" . }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $name }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
data:
  job.json: {{ $job | toJson | quote }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $name }}
  {{- with include "ploeg.executorNamespaceMetadata" . }}
  {{ . }}
  {{- end }}
  labels:
    app.kubernetes.io/name: {{ $name }}
    {{- include "ploeg.componentLabels" (dict "root" .) | nindent 4 }}
  {{- with $r.annotations }}
  annotations: {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ $o.replicas }}
  selector:
    matchLabels: {{- toYaml $selector | nindent 6 }}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: {{ $name }}
        app.kubernetes.io/part-of: ploeg
        ploeg.webgrip.dev/component: claude-runner-orchestrator
        {{- if and (include "ploeg.credentialGatewayEnabled" .) .Values.executor.credentialGateway.acknowledgeSingleNode }}
        ploeg.webgrip.dev/credential-gateway-shared-node: "true"
        {{- end }}
      annotations:
        checksum/job: {{ $job | toJson | sha256sum }}
        {{- with $r.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ $name }}
      automountServiceAccountToken: true
      enableServiceLinks: false
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets: {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $o.nodeSelector }}
      nodeSelector: {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $o.tolerations }}
      tolerations: {{- toYaml . | nindent 8 }}
      {{- end }}
      affinity: {{- include "ploeg.offRunNodesAffinity" (dict "root" . "self" $selector) | nindent 8 }}
      terminationGracePeriodSeconds: {{ $o.terminationGracePeriodSeconds }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        runAsGroup: 1000
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault
      {{- if $cc.install }}
      initContainers:
        - {{- include "ploeg.claudeCodeInstallContainer" . | nindent 10 }}
      {{- end }}
      containers:
        - name: orchestrator
          image: {{ include "ploeg.claudeRunnerImage" . }}
          args:
            - self-hosted-runner
            - orchestrator
            - --environment-secret-file
            - /var/run/secrets/claude-environment/environment-secret
            - --hooks-dir
            - /opt/ploeg/claude-runner/orchestrator-hooks
            {{- range (concat ($o.args | default list) ($o.extraArgs | default list)) }}
            - {{ . | quote }}
            {{- end }}
          env:
            - name: PLOEG_CLAUDE_RUNNER_JOB
              value: /etc/ploeg/claude-runner/job.json
            - name: PLOEG_CLAUDE_RUNNER_JOB_PREFIX
              value: {{ include "ploeg.claudeRunnerName" . | trunc 46 | trimSuffix "-" | quote }}
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            {{- with (include "ploeg.claudeCodeEnv" . | fromJsonArray) }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          ports:
            - name: health
              containerPort: 8080
          readinessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet: {path: /healthz, port: health}
            initialDelaySeconds: 30
            periodSeconds: 30
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
          {{- with $o.resources }}
          resources: {{- toYaml . | nindent 12 }}
          {{- end }}
          volumeMounts:
            - name: environment-secret
              mountPath: /var/run/secrets/claude-environment
              readOnly: true
            - name: job
              mountPath: /etc/ploeg/claude-runner
              readOnly: true
            {{- if $cc.install }}
            - name: claude-code
              mountPath: /opt/claude-code
              readOnly: true
            {{- end }}
      volumes:
        - name: environment-secret
          secret:
            secretName: {{ $r.environmentSecret.name }}
            items:
              - key: SELF_HOSTED_RUNNER_ENVIRONMENT_SECRET
                path: environment-secret
        - name: job
          configMap:
            name: {{ $name }}
        {{- if $cc.install }}
        - name: claude-code
          emptyDir:
            sizeLimit: 1Gi
        {{- end }}
{{- end -}}
