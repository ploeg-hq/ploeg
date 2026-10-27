{{- define "ploeg.netpol.endpointRule" -}}
{{- $np := .root.Values.networkPolicy }}
{{- $endpoint := .endpoint | default dict }}
{{- $u := urlParse .url }}
{{- $hostname := $u.hostname }}
{{- $port := $endpoint.port | default "" | toString }}
{{- if not $port }}
{{- $hostPort := splitList ":" $u.host }}
{{- if gt (len $hostPort) 1 }}
{{- $port = last $hostPort }}
{{- else if eq $u.scheme "https" }}
{{- $port = "443" }}
{{- else }}
{{- $port = "80" }}
{{- end }}
{{- end }}
{{- $to := $endpoint.peers | default list }}
{{- if not $to }}
{{- $labels := splitList "." $hostname }}
{{- if regexMatch "^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+$" $hostname }}
{{- $to = list (dict "ipBlock" (dict "cidr" (printf "%s/32" $hostname))) }}
{{- else if eq (len $labels) 1 }}
{{- $to = list (dict "podSelector" dict) }}
{{- else if and (ge (len $labels) 3) (eq (index $labels 2) "svc") }}
{{- $to = list (dict "namespaceSelector" (dict "matchLabels" (dict "kubernetes.io/metadata.name" (index $labels 1)))) }}
{{- else }}
{{- $to = list (include "ploeg.netpol.internetPeer" .root | fromJson) }}
{{- end }}
{{- end }}
{{- dict "to" $to "ports" (list (dict "protocol" "TCP" "port" (int $port))) | toJson }}
{{- end -}}

{{- define "ploeg.netpol.internetPeer" -}}
{{- $block := dict "cidr" "0.0.0.0/0" }}
{{- with .Values.networkPolicy.privateCidrs }}
{{- $_ := set $block "except" . }}
{{- end }}
{{- dict "ipBlock" $block | toJson }}
{{- end -}}

{{- define "ploeg.netpol.internetRule" -}}
{{- dict "to" (list (include "ploeg.netpol.internetPeer" .root | fromJson)) "ports" (list (dict "protocol" "TCP" "port" (int .port))) | toJson }}
{{- end -}}

{{- define "ploeg.netpol.dnsRule" -}}
{{- $dns := .Values.networkPolicy.dns }}
{{- dict "to" $dns.peers "ports" $dns.ports | toJson }}
{{- end -}}

{{- define "ploeg.netpol.ploegdRule" -}}
{{- if .url }}
{{- include "ploeg.netpol.endpointRule" (dict "root" .root "url" .url "endpoint" dict) }}
{{- else }}
{{- $labels := include "ploeg.selectorLabels" .root | fromYaml }}
{{- $_ := set $labels "ploeg.webgrip.dev/component" "daemon" }}
{{- $peer := dict "podSelector" (dict "matchLabels" $labels) }}
{{- with .namespace }}{{- $_ := set $peer "namespaceSelector" (dict "matchLabels" (dict "kubernetes.io/metadata.name" .)) }}{{- end }}
{{- dict "to" (list $peer) "ports" (list (dict "protocol" "TCP" "port" 8080)) | toJson }}
{{- end }}
{{- end -}}

{{- define "ploeg.netpol.rules" -}}
{{- $seen := dict }}
{{- $out := list }}
{{- range .rules }}
{{- if and . (not (hasKey $seen .)) }}
{{- $_ := set $seen . true }}
{{- $out = append $out (fromJson .) }}
{{- end }}
{{- end }}
{{- range .extra }}
{{- $out = append $out . }}
{{- end }}
{{- $out | toJson }}
{{- end -}}

{{- define "ploeg.netpol.forgeUrls" -}}
{{- $ex := .root.Values.executor }}
{{- $urls := list }}
{{- if eq .side "worker" }}
{{- with (get $ex ($ex.forge | default "forgejo") | default dict).url }}{{- $urls = append $urls . }}{{- end }}
{{- else }}
{{- range $name := list "forgejo" "gitlab" "github" }}
{{- $f := get $ex $name | default dict }}
{{- with $f.publicUrl | default $f.url }}{{- $urls = append $urls . }}{{- end }}
{{- end }}
{{- with $ex.github.url }}
{{- if eq (trimSuffix "/" .) "https://github.com" }}
{{- $urls = append $urls "https://api.github.com" }}
{{- end }}
{{- end }}
{{- end }}
{{- $urls | toJson }}
{{- end -}}

{{- define "ploeg.netpol.trackerUrls" -}}
{{- $t := .Values.tracker }}
{{- $urls := list }}
{{- with $t.url }}{{- $urls = append $urls . }}{{- end }}
{{- if or ($t.clickup.tokenSecret | default dict).name ($t.clickup.webhookSecret | default dict).name }}
{{- $urls = append $urls ($t.clickup.url | default "https://api.clickup.com") }}
{{- end }}
{{- if $t.github.appId }}
{{- $urls = append $urls ($t.github.apiUrl | default "https://api.github.com") }}
{{- end }}
{{- $urls | toJson }}
{{- end -}}

{{- define "ploeg.netpol.telemetryUrls" -}}
{{- $urls := list }}
{{- $blocks := list (.Values.executor.harness | default dict) }}
{{- range $team := .Values.executor.teams }}
{{- $blocks = append $blocks ($team.harness | default dict) }}
{{- range $team.harnesses }}{{- $blocks = append $blocks . }}{{- end }}
{{- range $round := $team.plan }}
{{- range $round.roles }}{{- $blocks = append $blocks (.harness | default dict) }}{{- end }}
{{- end }}
{{- end }}
{{- range $blocks }}
{{- with (.telemetry | default dict).endpoint }}{{- $urls = append $urls . }}{{- end }}
{{- end }}
{{- $urls | toJson }}
{{- end -}}

{{- define "ploeg.netpol.claudeEnvironmentTeams" -}}
{{- $root := . }}
{{- $teams := list }}
{{- if .Values.executor.enabled }}
{{- range $team := .Values.executor.teams }}
{{- $found := false }}
{{- range $role := (include "ploeg.teamRoles" $team | fromJsonArray) }}
{{- $identity := include "ploeg.roleHarness" (dict "root" $root "team" $team "role" $role) | fromJson }}
{{- $ce := include "ploeg.claudeEnvironment" (dict "root" $root "team" $team "role" $role) | fromJson }}
{{- if and (eq $identity.name "claude-environment") (ne $ce.carrier "client") }}{{- $found = true }}{{- end }}
{{- end }}
{{- if $found }}{{- $teams = append $teams $team.name }}{{- end }}
{{- end }}
{{- end }}
{{- $teams | toJson }}
{{- end -}}

{{- define "ploeg.netpol.name" -}}
{{- printf "%s-%s" (include "ploeg.fullname" .root) .suffix | trunc 63 | trimSuffix "-" -}}
{{- end -}}
