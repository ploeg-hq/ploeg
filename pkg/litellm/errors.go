package litellm

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ErrorKind names what a failed call to a LiteLLM gateway means.
// ErrorBudgetExceeded is a LiteLLM spend limit; ErrorProviderQuota is the
// provider's own billing or quota; ErrorKeyRejected is the gateway refusing
// the caller's key; ErrorProviderAuth is the provider refusing the gateway's
// credential; ErrorModelUnavailable is a model the key may not use or that
// does not exist; ErrorBadRequest is any other refused request; ErrorUnknown
// is an answer with no recognised status or wording.
type ErrorKind string

// The kinds of gateway failure ClassifyError returns.
const (
	ErrorBudgetExceeded      ErrorKind = "budget_exceeded"
	ErrorRateLimited         ErrorKind = "rate_limited"
	ErrorProviderQuota       ErrorKind = "provider_quota"
	ErrorKeyRejected         ErrorKind = "key_rejected"
	ErrorProviderAuth        ErrorKind = "provider_auth"
	ErrorModelUnavailable    ErrorKind = "model_unavailable"
	ErrorContextTooLong      ErrorKind = "context_too_long"
	ErrorContentFiltered     ErrorKind = "content_filtered"
	ErrorTimeout             ErrorKind = "timeout"
	ErrorProviderUnavailable ErrorKind = "provider_unavailable"
	ErrorBadRequest          ErrorKind = "bad_request"
	ErrorUnknown             ErrorKind = "unknown"
)

// BudgetOwner names whose LiteLLM spend limit was reached, as the gateway
// named it. The empty owner means the gateway did not say.
type BudgetOwner string

// The owners a LiteLLM budget message names.
const (
	BudgetOfKey          BudgetOwner = "key"
	BudgetOfTeam         BudgetOwner = "team"
	BudgetOfUser         BudgetOwner = "user"
	BudgetOfEndUser      BudgetOwner = "end_user"
	BudgetOfOrganization BudgetOwner = "organization"
	BudgetOfProject      BudgetOwner = "project"
	BudgetOfTag          BudgetOwner = "tag"
	BudgetOfModelGroup   BudgetOwner = "model_group"
)

// GatewayError is one classified gateway failure: its kind, whose budget for
// ErrorBudgetExceeded and that owner's id as the gateway named it, and
// LiteLLM's error type when the body carried one.
type GatewayError struct {
	Kind          ErrorKind
	BudgetOwner   BudgetOwner
	BudgetSubject string
	Type          string
}

// SharedBudgetSpent reports whether g is a spent LiteLLM budget that the
// gateway named and that is wider than the caller's own key: a team, user,
// end user, organization, project, tag or model access group budget. Every
// key under that owner is refused until a person raises or resets the
// budget, so a fresh key does not help.
func (g GatewayError) SharedBudgetSpent() bool {
	return g.Kind == ErrorBudgetExceeded && g.BudgetOwner != "" && g.BudgetOwner != BudgetOfKey
}

// BudgetSpent says whose LiteLLM budget is spent, such as "the LiteLLM team
// budget for t2 is spent". It returns "" unless g is ErrorBudgetExceeded.
func (g GatewayError) BudgetSpent() string {
	if g.Kind != ErrorBudgetExceeded {
		return ""
	}
	if g.BudgetOwner == "" {
		return "a LiteLLM budget is spent"
	}
	s := "the LiteLLM " + g.BudgetOwner.label() + " budget"
	if g.BudgetSubject != "" {
		s += " for " + g.BudgetSubject
	}
	return s + " is spent"
}

func (o BudgetOwner) label() string {
	switch o {
	case BudgetOfEndUser:
		return "end user"
	case BudgetOfModelGroup:
		return "model access group"
	}
	return string(o)
}

var (
	budgetTypes = map[string]bool{"budget_exceeded": true}
	keyTypes    = map[string]bool{"auth_error": true, "expired_key": true, "token_not_found_in_db": true}
	rateTypes   = map[string]bool{"throttling_error": true, "rate_limit_error": true, "rate_limit_exceeded": true}
	quotaTypes  = map[string]bool{"insufficient_quota": true, "billing_error": true}

	budgetWords  = regexp.MustCompile(`(?i)budget has been exceeded|exceeded ?budget|budget[_ ]?exceeded|crossed budget|over (?:its |the )?budget|max budget`)
	quotaWords   = regexp.MustCompile(`(?i)insufficient_quota|exceeded your current quota|insufficient (?:credits|balance|funds)|credit balance is too low|billing`)
	modelWords   = regexp.MustCompile("(?i)_model_access_denied|not allowed to access model|is not available for this api key|invalid model name|model_not_found|model .{0,80} does not exist|litellm\\.NotFoundError")
	contextWords = regexp.MustCompile(`(?i)ContextWindowExceeded|context_length_exceeded|maximum context length|context window|context length|(?:prompt|input) is too long`)
	contentWords = regexp.MustCompile(`(?i)ContentPolicyViolation|content_policy_violation|content management policy|content_filter`)
	authWords    = regexp.MustCompile(`(?i)litellm\.AuthenticationError`)
	rateWords    = regexp.MustCompile(`(?i)RateLimitError|rate limit|too many requests|no healthy deployments?|no deployments available`)
	timeoutWords = regexp.MustCompile(`(?i)litellm\.Timeout|APITimeoutError|timed? ?out`)

	budgetOwners = []struct {
		pattern *regexp.Regexp
		owner   BudgetOwner
	}{
		{regexp.MustCompile(`\bKey[= ]|\bKey over\b`), BudgetOfKey},
		{regexp.MustCompile(`\bEnd User=([^\s,;"\\]+)`), BudgetOfEndUser},
		{regexp.MustCompile(`\bTeamMember=([^\s,;"\\]+)|\bUser=([^\s,;"\\]+) in Team=`), BudgetOfUser},
		{regexp.MustCompile(`\bTeam=([^\s,;"\\]+)`), BudgetOfTeam},
		{regexp.MustCompile(`\bUser=([^\s,;"\\]+)`), BudgetOfUser},
		{regexp.MustCompile(`\bOrganization=([^\s,;"\\]+)`), BudgetOfOrganization},
		{regexp.MustCompile(`\bProject=([^\s,;"\\]+)`), BudgetOfProject},
		{regexp.MustCompile(`\bTag=([^\s,;"\\]+)`), BudgetOfTag},
		{regexp.MustCompile(`(?i)\bModel access group=([^\s,;"\\]+)`), BudgetOfModelGroup},
	}
)

// ClassifyError classifies a failed call to a LiteLLM gateway from its HTTP
// status and response body. LiteLLM's error type and wording decide first and
// the status only when they say nothing, so a spent budget is recognised
// whichever status the gateway gives it: LiteLLM answered 429 up to 1.103 and
// 422 from 1.104, and 400 or 402 elsewhere. A status of 0 classifies text
// with no HTTP answer, such as a harness's stderr, by its wording alone.
func ClassifyError(status int, body []byte) GatewayError {
	typ, message := errorParts(body)
	text := typ + " " + message
	known := func(kind ErrorKind) GatewayError { return GatewayError{Kind: kind, Type: typ} }
	switch {
	case budgetTypes[typ] || budgetWords.MatchString(text):
		owner, subject := budgetOwner(message)
		return GatewayError{Kind: ErrorBudgetExceeded, BudgetOwner: owner, BudgetSubject: subject, Type: typ}
	case modelWords.MatchString(text):
		return known(ErrorModelUnavailable)
	case keyTypes[typ]:
		return known(ErrorKeyRejected)
	case contextWords.MatchString(text):
		return known(ErrorContextTooLong)
	case contentWords.MatchString(text):
		return known(ErrorContentFiltered)
	case quotaTypes[typ] || quotaWords.MatchString(text):
		return known(ErrorProviderQuota)
	case typ == "authentication_error" || authWords.MatchString(message):
		return known(ErrorProviderAuth)
	case rateTypes[typ] || rateWords.MatchString(text):
		return known(ErrorRateLimited)
	case typ == "timeout" || status == 408 || status == 504 || (status >= 500 && timeoutWords.MatchString(message)):
		return known(ErrorTimeout)
	case status == 401 || status == 403:
		return known(ErrorKeyRejected)
	case status == 402:
		return known(ErrorBudgetExceeded)
	case status == 404:
		return known(ErrorModelUnavailable)
	case status == 429:
		return known(ErrorRateLimited)
	case status >= 500:
		return known(ErrorProviderUnavailable)
	case status >= 400:
		return known(ErrorBadRequest)
	}
	return known(ErrorUnknown)
}

// Describe says in a few words what kind of gateway failure k is, for a Run's
// summary. It returns "" for ErrorUnknown.
func (k ErrorKind) Describe() string {
	switch k {
	case ErrorBudgetExceeded:
		return "a LiteLLM budget is spent"
	case ErrorRateLimited:
		return "the model gateway or provider is rate limiting"
	case ErrorProviderQuota:
		return "the model provider's quota or credit is used up"
	case ErrorKeyRejected:
		return "the model gateway refused the Run's key"
	case ErrorProviderAuth:
		return "the model provider refused the gateway's credential"
	case ErrorModelUnavailable:
		return "the model is not available to the Run's key"
	case ErrorContextTooLong:
		return "the prompt is longer than the model's context window"
	case ErrorContentFiltered:
		return "the provider refused the prompt under its content rules"
	case ErrorTimeout:
		return "the model gateway timed out"
	case ErrorProviderUnavailable:
		return "the model provider is unavailable"
	case ErrorBadRequest:
		return "the model gateway refused the request"
	}
	return ""
}

func errorParts(body []byte) (typ, message string) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", string(body)
	}
	root, _ := data.(map[string]any)
	var inner any = data
	if root != nil {
		if e, ok := root["error"]; ok {
			inner = e
		} else if d, ok := root["detail"]; ok {
			inner = d
		}
	}
	switch e := inner.(type) {
	case string:
		return "", e
	case map[string]any:
		if nested, ok := e["error"].(map[string]any); ok {
			e = nested
		}
		typ, _ = e["type"].(string)
		message, _ = e["message"].(string)
		if s, ok := e["error"].(string); ok {
			message = strings.TrimSpace(message + " " + s)
		}
	}
	if message == "" {
		message = string(body)
	}
	return typ, message
}

func budgetOwner(message string) (BudgetOwner, string) {
	for _, o := range budgetOwners {
		m := o.pattern.FindStringSubmatch(message)
		if m == nil {
			continue
		}
		for _, subject := range m[1:] {
			if subject = strings.TrimRight(subject, ".:!"); subject != "" {
				return o.owner, subject
			}
		}
		return o.owner, ""
	}
	return "", ""
}
