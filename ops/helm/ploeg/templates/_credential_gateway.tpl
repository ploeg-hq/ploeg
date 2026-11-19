{{/*
ploeg.credentialGatewayEnabled is "true" when the executor renders Run pods
in gateway mode (ADR-0197): no credential enters a Run pod, and the
credential gateway outside it attaches each one a Run uses.
*/}}
{{- define "ploeg.credentialGatewayEnabled" -}}
{{- $e := .Values.executor -}}
{{- if and $e.enabled ($e.credentialGateway | default dict).enabled -}}true{{- end -}}
{{- end -}}

{{- define "ploeg.credentialGatewayName" -}}
{{- printf "%s-credential-gateway" (include "ploeg.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ploeg.credentialGatewaySelectorLabels" -}}
app.kubernetes.io/name: ploeg-credential-gateway
app.kubernetes.io/instance: {{ include "ploeg.instance" . }}
{{- end -}}

{{/*
ploeg.credentialGatewayUrls are the URLs a Run pod calls in gateway mode, as
JSON {control, forge, model, mcp}. The model URL keeps the path of
executor.litellm.baseUrl, so a harness's base URL ends where it did.
*/}}
{{- define "ploeg.credentialGatewayUrls" -}}
{{- $host := printf "%s.%s.svc" (include "ploeg.credentialGatewayName" .) (include "ploeg.executorNamespace" .) -}}
{{- $path := (urlParse .Values.executor.litellm.baseUrl).path -}}
{{- dict "control" (printf "http://%s:8080" $host) "forge" (printf "http://%s:8081" $host) "model" (printf "http://%s:8082%s" $host $path) "mcp" (printf "http://%s:8083" $host) | toJson -}}
{{- end -}}

{{/*
ploeg.credentialGatewayBootstrapKeys lists the bootstrap Secret keys of the
teams and Roles this release renders, <team>--<role or default>, as JSON.
*/}}
{{- define "ploeg.credentialGatewayBootstrapKeys" -}}
{{- $keys := list -}}
{{- range $team := .Values.executor.teams -}}
{{- range $role := (include "ploeg.teamRoles" $team | fromJsonArray) -}}
{{- $keys = append $keys (printf "%s--%s" $team.name ($role.name | default "default")) -}}
{{- end -}}
{{- end -}}
{{- $keys | uniq | toJson -}}
{{- end -}}

{{/*
ploeg.requireCredentialGatewayValues fails the render of gateway mode with a
value that would put a secret into a Run pod or that the gateway cannot serve.
*/}}
{{- define "ploeg.requireCredentialGatewayValues" -}}
{{- $root := . -}}
{{- $auth := .Values.executor.workerAuth -}}
{{- if eq $auth.mode "legacy" -}}
{{- fail "executor.credentialGateway.enabled needs executor.workerAuth.mode managed: legacy mode hands a Run pod the static model key of executor.workerAuth.staticInferenceSecret, and no secret enters a Run pod (ADR-0197)" -}}
{{- end -}}
{{- if eq ($auth.hmacCapability | default "issue") "off" -}}
{{- fail "executor.credentialGateway.enabled needs executor.workerAuth.hmacCapability issue: the per-Run capability the gateway accepts is the HMAC controlToken, which hmacCapability off stops issuing (ADR-0197)" -}}
{{- end -}}
{{- if not (.Values.networkPolicy | default dict).enabled -}}
{{- fail "executor.credentialGateway.enabled needs networkPolicy.enabled: without the Run pod's NetworkPolicy a Run pod reaches ploegd, the forge and the model gateway directly, around the credential gateway (ADR-0197)" -}}
{{- end -}}
{{- if include "ploeg.runtimePools" . | fromJsonArray -}}
{{- fail "executor.runtimeTeams.pools cannot run with executor.credentialGateway.enabled: a pool's worker holds a pool bootstrap and the in-worker credential proxies, and no secret enters a Run pod (ADR-0197, ADR-0198)" -}}
{{- end -}}
{{- if and .Values.claudeRunner.enabled (not (include "ploeg.claudeRunnerOrchestrator" .)) -}}
{{- fail "claudeRunner.mode fleet cannot run with executor.credentialGateway.enabled: a fixed fleet mounts claudeRunner.environmentSecret into the pod where sessions run, and no secret enters a pod where an agent runs (ADR-0197 section 7). Set claudeRunner.mode: orchestrator" -}}
{{- end -}}
{{- include "ploeg.refuseClaudeLogins" . -}}
{{- if and (include "ploeg.anyTeamOnSandbox" .) (.Values.executor.sandbox | default dict).networkPolicy -}}
{{- fail "executor.sandbox.networkPolicy cannot be set with executor.credentialGateway.enabled: its rules are added to the Run pod's egress, and a gateway-mode Run pod reaches only DNS and the credential gateway (ADR-0197)" -}}
{{- end -}}
{{- range $team := .Values.executor.teams -}}
{{- range $planRole := (include "ploeg.teamRoles" $team | fromJsonArray) -}}
{{- range $w := (include "ploeg.roleWorkloads" (dict "root" $root "team" $team "role" $planRole) | fromJsonArray) -}}
{{- $who := $team.name -}}{{- with $w.role.name -}}{{- $who = printf "%s/%s" $team.name . -}}{{- end -}}
{{- $harness := (include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $w.role) | fromJson).name -}}
{{- if eq (include "ploeg.roleUsesDind" (dict "root" $root "team" $team "role" $w.role)) "true" -}}
{{- fail (printf "team %s: harness.dind cannot run with executor.credentialGateway.enabled: DinD runs a privileged Docker daemon in the Run pod and hands the harness its client certificates (DOCKER_HOST, DOCKER_CERT_PATH, /certs), which is root on the node, and no secret enters a Run pod (ADR-0197). Set harness.dind: false at every applicable override" $who) -}}
{{- end -}}
{{- if and (eq $harness "claude-environment") (eq ($w.carrier | default "") "routine") (not (include "ploeg.claudeRunnerOrchestrator" $root)) -}}
{{- fail (printf "team %s: harness.claudeEnvironment.carrier routine with executor.credentialGateway.enabled needs claudeRunner.enabled with claudeRunner.mode orchestrator: a routine session clones and pushes only through this gateway, from a runner pod its orchestrator started (ADR-0197 section 7)" $who) -}}
{{- end -}}
{{- if and (eq $harness "claude-environment") (ne ($w.carrier | default "") "routine") -}}
{{- fail (printf "team %s: harness claude-environment cannot run with executor.credentialGateway.enabled unless harness.claudeEnvironment.carrier is routine: a worker or a person's client that dispatches the session holds a Claude login, and in a gateway namespace a session starts only through the person's own routine (ADR-0197 section 7)" $who) -}}
{{- end -}}
{{- if eq $harness "claude-code" -}}
{{- range $p := (include "ploeg.roleClaudePlugins" (dict "root" $root "team" $team "role" $w.role) | fromJson).plugins -}}
{{- range $e := ($p.env | default list) -}}
{{- if $e.secret -}}
{{- fail (printf "team %s: Claude Code plugin %s reads %s from Secret %s, and with executor.credentialGateway.enabled no Secret enters a Run pod (ADR-0197). Give the variable a value or leave it out" $who $p.name $e.name $e.secret.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "ploeg.refuseClaudeLogins" -}}
{{- $levels := list (dict "who" "executor" "harness" (.Values.executor.harness | default dict)) -}}
{{- range $team := .Values.executor.teams -}}
{{- $levels = append $levels (dict "who" (printf "team %s" $team.name) "harness" ($team.harness | default dict)) -}}
{{- range $role := (include "ploeg.teamRoles" $team | fromJsonArray) -}}
{{- with $role.name -}}
{{- $levels = append $levels (dict "who" (printf "team %s/%s" $team.name .) "harness" ($role.harness | default dict)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $level := $levels -}}
{{- $ce := $level.harness.claudeEnvironment | default dict -}}
{{- range $field := list "loginVolume" "loginSecret" -}}
{{- if index $ce $field -}}
{{- fail (printf "%s: harness.claudeEnvironment.%s cannot be set with executor.credentialGateway.enabled: it puts a person's Claude login into a pod where an agent runs, and Ploeg stores, relays and mounts no Claude login (ADR-0197 section 7). A routine Run starts through the person's own routine (carrier: routine)" $level.who $field) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
ploeg.requireCredentialFreeRunPod fails the render of a gateway-mode Run pod
that would still receive a secret by any means: a secretKeyRef, an envFrom
Secret, a ServiceAccount token, a volume of any type but emptyDir and image,
a privileged container or a host namespace, or a Docker daemon's client
certificates. It reads the rendered pod, so a source the values checks above
do not know is refused too.
Context: (dict "pod" <pod template as a dict> "who" <team or team/role>).
*/}}
{{- define "ploeg.requireCredentialFreeRunPod" -}}
{{- $spec := .pod.spec -}}
{{- if ne (toString $spec.automountServiceAccountToken) "false" -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders a Run pod without automountServiceAccountToken: false; no ServiceAccount token enters a Run pod (ADR-0197)" .who) -}}
{{- end -}}
{{- range $field := list "hostNetwork" "hostPID" "hostIPC" -}}
{{- if index $spec $field -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders a Run pod with %s; a Run pod shares no namespace with its node (ADR-0197)" $.who $field) -}}
{{- end -}}
{{- end -}}
{{- range $c := concat ($spec.initContainers | default list) ($spec.containers | default list) -}}
{{- if ($c.securityContext | default dict).privileged -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders container %s privileged; a privileged container is root on the node, and no secret enters a Run pod (ADR-0197)" $.who $c.name) -}}
{{- end -}}
{{- range $e := ($c.env | default list) -}}
{{- if ($e.valueFrom | default dict).secretKeyRef -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders %s into container %s from a Secret; no Secret enters a Run pod (ADR-0197)" $.who $e.name $c.name) -}}
{{- end -}}
{{- if has $e.name (list "DOCKER_HOST" "DOCKER_CERT_PATH" "DOCKER_TLS_VERIFY") -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders %s into container %s; a Docker daemon's client certificates are root on the node, and no secret enters a Run pod (ADR-0197)" $.who $e.name $c.name) -}}
{{- end -}}
{{- end -}}
{{- range $m := ($c.volumeMounts | default list) -}}
{{- if or (eq $m.mountPath "/certs") (hasPrefix "/certs/" $m.mountPath) -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled mounts %s into container %s at %s, where a Docker daemon keeps its client certificates; no secret enters a Run pod (ADR-0197)" $.who $m.name $c.name $m.mountPath) -}}
{{- end -}}
{{- end -}}
{{- range $e := ($c.envFrom | default list) -}}
{{- if $e.secretRef -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders container %s with envFrom Secret %s; no Secret enters a Run pod (ADR-0197)" $.who $c.name $e.secretRef.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $v := ($spec.volumes | default list) -}}
{{- if or (hasKey $v "secret") (hasKey $v "projected") -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders volume %s from a Secret or a projection; no Secret enters a Run pod (ADR-0197)" $.who $v.name) -}}
{{- end -}}
{{- $types := without (keys $v | sortAlpha) "name" -}}
{{- if or (ne (len $types) 1) (not (has (first $types) (list "emptyDir" "image"))) -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders volume %s of type %s; a Run pod mounts only emptyDir and image volumes, since any other can carry a credential in (ADR-0197)" $.who $v.name (join ", " $types)) -}}
{{- end -}}
{{- if hasPrefix "docker-" $v.name -}}
{{- fail (printf "team %s: executor.credentialGateway.enabled renders volume %s, a Docker daemon's; no secret enters a Run pod (ADR-0197)" $.who $v.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
ploeg.runPodTemplate is ploeg.workerPodTemplate, checked in gateway mode by
ploeg.requireCredentialFreeRunPod. Every executor renders its Run pod through
it. Same context as ploeg.workerPodTemplate.
*/}}
{{- define "ploeg.runPodTemplate" -}}
{{- $pod := include "ploeg.workerPodTemplate" . -}}
{{- if include "ploeg.credentialGatewayEnabled" .root -}}
{{- $who := .team.name -}}{{- with (.role | default dict).name -}}{{- $who = printf "%s/%s" $who . -}}{{- end -}}
{{- include "ploeg.requireCredentialFreeRunPod" (dict "pod" ($pod | fromYaml) "who" $who) -}}
{{- end -}}
{{- $pod -}}
{{- end -}}

{{- define "ploeg.credentialGatewayAffinity" -}}
{{- include "ploeg.offRunNodesAffinity" (dict "root" . "self" (include "ploeg.credentialGatewaySelectorLabels" . | fromYaml)) -}}
{{- end -}}

{{/*
ploeg.mcpAdapterHosts are the cluster hosts of the stdio MCP adapters this
release renders, which toolRegistry.plainHTTPHosts must list to register
them (ADR-0199 section 7).
*/}}
{{- define "ploeg.mcpAdapterHosts" -}}
{{- $root := . -}}
{{- $hosts := list -}}
{{- range (((.Values.executor.credentialGateway | default dict).mcp | default dict).adapters | default list) -}}
{{- $hosts = append $hosts (printf "%s.%s.svc" (printf "%s-mcp-%s" (include "ploeg.fullname" $root) .name | trunc 63 | trimSuffix "-") (include "ploeg.executorNamespace" $root)) -}}
{{- end -}}
{{- $hosts | toJson -}}
{{- end -}}
