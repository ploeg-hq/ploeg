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
            {{- if $gateway }}
            - name: PLOEG_CREDENTIAL_GATEWAY
              value: "on"
            {{- end }}
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
{{- if not (semverCompare ">=1.30.0-0" .Capabilities.KubeVersion.Version) -}}
{{- fail "claudeRunner.mode orchestrator needs Kubernetes >= 1.30: a ValidatingAdmissionPolicy lets the orchestrator's ServiceAccount create only the chart's runner Job" -}}
{{- end -}}
{{- include "ploeg.claudeRunnerJobPolicy" . }}
---
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

{{- define "ploeg.claudeRunnerContainerRule" -}}
{{- $c := .container -}}
{{- $sc := $c.securityContext | default dict -}}
{{- $pairs := list -}}
{{- range $e := ($c.env | default list) -}}{{- $pairs = append $pairs (list $e.name (toString ($e.value | default ""))) -}}{{- end -}}
{{- $mounts := list -}}
{{- $readOnly := list -}}
{{- range $m := ($c.volumeMounts | default list) -}}
{{- $mounts = append $mounts (list $m.name $m.mountPath) -}}
{{- if $m.readOnly }}{{- $readOnly = append $readOnly $m.name -}}{{- end -}}
{{- end -}}
{{- $envSize := len $pairs -}}
{{- $env := printf "c.env.all(e, %s.exists(p, p[0] == e.name && p[1] == (has(e.value) ? e.value : '')))" (toJson $pairs) -}}
{{- if .workOrder -}}
{{- $envSize = add $envSize 1 -}}
{{- $env = printf "c.env.exists_one(e, e.name == 'PLOEG_CLAUDE_RUNNER_WORK_ORDER') && c.env.all(e, e.name == 'PLOEG_CLAUDE_RUNNER_WORK_ORDER' || %s.exists(p, p[0] == e.name && p[1] == (has(e.value) ? e.value : '')))" (toJson $pairs) -}}
{{- end -}}
{{- $user := "!has(c.securityContext.runAsUser)" -}}
{{- with $sc.runAsUser }}{{- $user = printf "has(c.securityContext.runAsUser) && c.securityContext.runAsUser == %v" . -}}{{- end -}}
{{- $parts := list
  (printf "c.image == %s" (toJson $c.image))
  (printf "has(c.command) && c.command == %s" (toJson ($c.command | default list)))
  (ternary (printf "has(c.args) && c.args == %s" (toJson $c.args)) "!has(c.args)" (not (empty $c.args)))
  "!has(c.envFrom) && has(c.env) && c.env.all(e, !has(e.valueFrom))"
  (printf "c.env.size() == %d" $envSize)
  $env
  (printf "has(c.volumeMounts) && c.volumeMounts.size() == %d && c.volumeMounts.all(m, %s.exists(x, x[0] == m.name && x[1] == m.mountPath) && (m.name in %s) == (has(m.readOnly) && m.readOnly) && !has(m.subPath) && !has(m.subPathExpr))" (len $mounts) (toJson $mounts) (toJson $readOnly))
  "has(c.securityContext) && has(c.securityContext.allowPrivilegeEscalation) && !c.securityContext.allowPrivilegeEscalation && (!has(c.securityContext.privileged) || !c.securityContext.privileged) && has(c.securityContext.capabilities) && has(c.securityContext.capabilities.drop) && c.securityContext.capabilities.drop == ['ALL'] && !has(c.securityContext.capabilities.add) && (!has(c.securityContext.runAsNonRoot) || c.securityContext.runAsNonRoot)"
  $user
  "!has(c.ports) || c.ports.all(p, !has(p.hostPort) || p.hostPort == 0)"
  "!has(c.lifecycle) && !has(c.workingDir)"
-}}
{{- $wrapped := list -}}
{{- range $parts }}{{- $wrapped = append $wrapped (printf "(%s)" .) -}}{{- end -}}
{{- printf "%s.exists_one(c, c.name == %s) && %s.all(c, c.name != %s || (%s))" .list (toJson $c.name) .list (toJson $c.name) (join " && " $wrapped) -}}
{{- end -}}

{{- define "ploeg.claudeRunnerJobPolicy" -}}
{{- $root := . -}}
{{- $job := include "ploeg.claudeRunnerJob" . | fromYaml -}}
{{- $pod := $job.spec.template.spec -}}
{{- $podMeta := $job.spec.template.metadata -}}
{{- $name := printf "%s-%s-claude-runner-jobs" (include "ploeg.fullname" .) (include "ploeg.executorNamespace" .) | trunc 253 | trimSuffix "-" -}}
{{- $orchestrator := printf "system:serviceaccount:%s:%s" (include "ploeg.executorNamespace" .) (include "ploeg.claudeRunnerOrchestratorName" .) -}}
{{- $podAnnotations := omit ($podMeta.annotations | default dict) "ploeg.webgrip.dev/claude-session" -}}
{{- $volumes := list -}}
{{- range $pod.volumes }}{{- $volumes = append $volumes .name -}}{{- end -}}
{{- $containers := list -}}
{{- range $pod.containers }}{{- $containers = append $containers .name -}}{{- end -}}
{{- $inits := list -}}
{{- range ($pod.initContainers | default list) }}{{- $inits = append $inits .name -}}{{- end -}}
{{- $sc := $pod.securityContext -}}
{{- $rules := list -}}
{{- $controllerLabels := list "batch.kubernetes.io/controller-uid" "controller-uid" "batch.kubernetes.io/job-name" "job-name" -}}
{{- $rules = append $rules (dict "m" "The runner Job carries exactly the labels of the chart's runner Job." "e" (printf "has(object.metadata.labels) && %s.all(k, k in object.metadata.labels && object.metadata.labels[k] == %s[k]) && object.metadata.labels.all(k, k in %s || k in %s)" (toJson $job.metadata.labels) (toJson $job.metadata.labels) (toJson $job.metadata.labels) (toJson $controllerLabels))) -}}
{{- $rules = append $rules (dict "m" "The runner Job carries no annotation but its order and session." "e" "!has(object.metadata.annotations) || object.metadata.annotations.all(k, k in ['ploeg.webgrip.dev/claude-order', 'ploeg.webgrip.dev/claude-session'])") -}}
{{- $rules = append $rules (dict "m" "The runner pod carries exactly the labels of the chart's runner Job." "e" (printf "has(object.spec.template.metadata.labels) && %s.all(k, k in object.spec.template.metadata.labels && object.spec.template.metadata.labels[k] == %s[k]) && object.spec.template.metadata.labels.all(k, k in %s || k in %s)" (toJson $podMeta.labels) (toJson $podMeta.labels) (toJson $podMeta.labels) (toJson $controllerLabels))) -}}
{{- $annotationRule := "has(object.spec.template.metadata.annotations) && object.spec.template.metadata.annotations.all(k, k == 'ploeg.webgrip.dev/claude-session')" -}}
{{- if $podAnnotations -}}
{{- $annotationRule = printf "has(object.spec.template.metadata.annotations) && object.spec.template.metadata.annotations.all(k, k == 'ploeg.webgrip.dev/claude-session' || (k in %s && object.spec.template.metadata.annotations[k] == %s[k]))" (toJson $podAnnotations) (toJson $podAnnotations) -}}
{{- end -}}
{{- $rules = append $rules (dict "m" "The runner pod carries no annotation but its session and the chart's podAnnotations." "e" $annotationRule) -}}
{{- $rules = append $rules (dict "m" "The runner Job runs one pod once, for as long as the chart's runner Job." "e" (printf "object.spec.backoffLimit == %v && (!has(object.spec.parallelism) || object.spec.parallelism == 1) && (!has(object.spec.completions) || object.spec.completions == 1) && (!has(object.spec.manualSelector) || !object.spec.manualSelector) && object.spec.activeDeadlineSeconds == %v && object.spec.ttlSecondsAfterFinished == %v" $job.spec.backoffLimit $job.spec.activeDeadlineSeconds $job.spec.ttlSecondsAfterFinished)) -}}
{{- $rules = append $rules (dict "m" "The runner pod runs as the session ServiceAccount without a Kubernetes API token." "e" (printf "variables.pod.serviceAccountName == %s && (!has(variables.pod.serviceAccount) || variables.pod.serviceAccount == %s) && has(variables.pod.automountServiceAccountToken) && !variables.pod.automountServiceAccountToken" (toJson $pod.serviceAccountName) (toJson $pod.serviceAccountName))) -}}
{{- $rules = append $rules (dict "m" "The runner pod mounts only the chart's emptyDir volumes: no Secret, projected, hostPath or PersistentVolumeClaim volume." "e" (printf "has(variables.pod.volumes) && variables.pod.volumes.size() == %d && variables.pod.volumes.all(v, has(v.emptyDir) && v.name in %s)" (len $volumes) (toJson $volumes))) -}}
{{- $rules = append $rules (dict "m" "The runner pod shares nothing with its node and is placed like the chart's runner Job." "e" (printf "(!has(variables.pod.hostNetwork) || !variables.pod.hostNetwork) && (!has(variables.pod.hostPID) || !variables.pod.hostPID) && (!has(variables.pod.hostIPC) || !variables.pod.hostIPC) && (!has(variables.pod.shareProcessNamespace) || !variables.pod.shareProcessNamespace) && !has(variables.pod.nodeName) && !has(variables.pod.affinity) && !has(variables.pod.tolerations) && !has(variables.pod.hostAliases) && variables.pod.restartPolicy == 'Never' && %s && %s" (ternary (printf "has(variables.pod.nodeSelector) && variables.pod.nodeSelector == %s" (toJson $pod.nodeSelector)) "!has(variables.pod.nodeSelector)" (not (empty $pod.nodeSelector))) (ternary (printf "has(variables.pod.runtimeClassName) && variables.pod.runtimeClassName == %s" (toJson $pod.runtimeClassName)) "!has(variables.pod.runtimeClassName)" (not (empty $pod.runtimeClassName))))) -}}
{{- $rules = append $rules (dict "m" "The runner pod runs as the chart's non-root user under RuntimeDefault seccomp." "e" (printf "has(variables.pod.securityContext) && has(variables.pod.securityContext.runAsNonRoot) && variables.pod.securityContext.runAsNonRoot && has(variables.pod.securityContext.runAsUser) && variables.pod.securityContext.runAsUser == %v && has(variables.pod.securityContext.seccompProfile) && variables.pod.securityContext.seccompProfile.type == 'RuntimeDefault' && !has(variables.pod.securityContext.sysctls)" $sc.runAsUser)) -}}
{{- $rules = append $rules (dict "m" "The runner pod runs exactly the chart's containers." "e" (printf "variables.pod.containers.size() == %d && variables.pod.containers.all(c, c.name in %s) && (has(variables.pod.initContainers) ? variables.pod.initContainers.size() : 0) == %d && !has(variables.pod.ephemeralContainers)" (len $containers) (toJson $containers) (len $inits))) -}}
{{- range $c := $pod.containers -}}
{{- $rules = append $rules (dict "m" (printf "Container %s is the chart's: its image, command, arguments, mounts and security context, and no environment but the chart's and the work order." $c.name) "e" (include "ploeg.claudeRunnerContainerRule" (dict "container" $c "list" "variables.pod.containers" "workOrder" (eq $c.name "runner")))) -}}
{{- end -}}
{{- range $c := ($pod.initContainers | default list) -}}
{{- $rules = append $rules (dict "m" (printf "Init container %s is the chart's: its image, command, mounts, security context and environment." $c.name) "e" (printf "has(variables.pod.initContainers) && %s" (include "ploeg.claudeRunnerContainerRule" (dict "container" $c "list" "variables.pod.initContainers" "workOrder" false)))) -}}
{{- end -}}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ $name }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
spec:
  failurePolicy: Fail
  matchConstraints:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: {{ include "ploeg.executorNamespace" . | quote }}
    resourceRules:
      - apiGroups: ["batch"]
        apiVersions: ["v1"]
        operations: [CREATE, UPDATE]
        resources: [jobs]
  matchConditions:
    - name: from-the-orchestrator
      expression: {{ printf "request.userInfo.username == %s" (toJson $orchestrator) | quote }}
  variables:
    - name: pod
      expression: "object.spec.template.spec"
  validations:
    {{- range $rules }}
    - expression: {{ .e | quote }}
      message: {{ .m | quote }}
    {{- end }}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ $name }}
  labels: {{- include "ploeg.labels" . | nindent 4 }}
spec:
  policyName: {{ $name }}
  validationActions: [Deny]
{{- end -}}
