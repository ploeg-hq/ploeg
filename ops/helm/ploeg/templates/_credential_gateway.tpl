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
{{- if .Values.claudeRunner.enabled -}}
{{- fail "claudeRunner.enabled cannot run with executor.credentialGateway.enabled: the runner pod, where a claude-environment session's Claude Code runs, holds claudeRunner.environmentSecret and receives the Run's forge token, and its route through the gateway's anthropic upstream is not built yet (ADR-0197)" -}}
{{- end -}}
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
{{- if eq $harness "claude-environment" -}}
{{- fail (printf "team %s: harness claude-environment cannot run with executor.credentialGateway.enabled: claude-environment's Anthropic traffic and pushes go through the gateway's anthropic upstream, which is not built yet, and its pod holds a person's login (ADR-0197)" $who) -}}
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

{{/*
ploeg.credentialGatewayAffinity keeps the gateway off every node that runs a
Run pod and out of executor.nodeSelector's pool, and spreads its replicas.
acknowledgeSingleNode relaxes the first two to preferred. One node selector
term per executor.nodeSelector entry: a node is excluded only when it carries
every entry, as a Run pod's node does.
*/}}
{{- define "ploeg.credentialGatewayAffinity" -}}
{{- $gw := .Values.executor.credentialGateway -}}
{{- $runPods := dict "labelSelector" (dict "matchLabels" (dict "app.kubernetes.io/name" "ploeg-worker")) "topologyKey" "kubernetes.io/hostname" -}}
{{- $replicas := dict "labelSelector" (dict "matchLabels" (include "ploeg.credentialGatewaySelectorLabels" . | fromYaml)) "topologyKey" "kubernetes.io/hostname" -}}
{{- $antiAffinity := dict -}}
{{- $preferred := list (dict "weight" 100 "podAffinityTerm" $replicas) -}}
{{- if $gw.acknowledgeSingleNode -}}
{{- $preferred = prepend $preferred (dict "weight" 100 "podAffinityTerm" $runPods) -}}
{{- else -}}
{{- $_ := set $antiAffinity "requiredDuringSchedulingIgnoredDuringExecution" (list $runPods) -}}
{{- end -}}
{{- $_ := set $antiAffinity "preferredDuringSchedulingIgnoredDuringExecution" $preferred -}}
{{- $affinity := dict "podAntiAffinity" $antiAffinity -}}
{{- with .Values.executor.nodeSelector -}}
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
