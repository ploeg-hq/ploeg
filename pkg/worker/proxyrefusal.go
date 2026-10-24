package worker

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type refusalReason string

const (
	refusalMissingPlaceholder  refusalReason = "missing_placeholder"
	refusalWrongPlaceholder    refusalReason = "wrong_placeholder"
	refusalUnsafePath          refusalReason = "unsafe_path"
	refusalOutsideRepository   refusalReason = "outside_repository"
	refusalForbiddenEndpoint   refusalReason = "forbidden_endpoint"
	refusalPushToOtherBranch   refusalReason = "push_to_other_branch"
	refusalBranchDeletion      refusalReason = "branch_deletion"
	refusalUninspectablePush   refusalReason = "uninspectable_push"
	refusalUninspectableBody   refusalReason = "uninspectable_body"
	refusalCredentialInRequest refusalReason = "credential_in_request"
	refusalWritesStoppedByLeak refusalReason = "writes_stopped_by_leak"
)

const (
	forgeProxyName = "forge"
	modelProxyName = "llm"
)

const (
	proxyRefusalMessage         = "credential proxy refused a request"
	forgeRejectedCredentialLine = "the forge refused the Run's credential behind the proxy"
	redactedInLog               = "[redacted]"
	maxLoggedRequestField       = 512
)

type refusalLog struct {
	proxy   string
	run     string
	log     *slog.Logger
	secrets func() []string
}

func (l refusalLog) refuse(w http.ResponseWriter, r *http.Request, reason refusalReason, message string) {
	l.record(r, reason)
	http.Error(w, message, http.StatusForbidden)
}

func (l refusalLog) record(r *http.Request, reason refusalReason) {
	if l.log == nil {
		return
	}
	l.log.Warn(proxyRefusalMessage, l.requestAttrs(r, "reason", string(reason))...)
}

func (l refusalLog) upstreamRejectedCredential(r *http.Request, status int) {
	if l.log == nil {
		return
	}
	l.log.Warn(forgeRejectedCredentialLine, l.requestAttrs(r, "status", status)...)
}

func (l refusalLog) requestAttrs(r *http.Request, extra ...any) []any {
	attrs := []any{"proxy", l.proxy, "run", l.run, "method", l.sanitised(r.Method), "path", l.sanitised(r.URL.EscapedPath())}
	return append(attrs, extra...)
}

func (l refusalLog) sanitised(field string) string {
	if l.secrets != nil {
		for _, secret := range l.secrets() {
			if secret == "" {
				continue
			}
			field = strings.ReplaceAll(field, secret, redactedInLog)
			field = strings.ReplaceAll(field, url.PathEscape(secret), redactedInLog)
			field = strings.ReplaceAll(field, url.QueryEscape(secret), redactedInLog)
		}
	}
	if len(field) > maxLoggedRequestField {
		field = field[:maxLoggedRequestField] + "..."
	}
	return field
}
