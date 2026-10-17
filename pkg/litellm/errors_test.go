package litellm

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

type capturedError struct {
	LiteLLM     string      `json:"litellm"`
	Case        string      `json:"case"`
	Status      int         `json:"status"`
	Body        string      `json:"body"`
	Kind        ErrorKind   `json:"kind"`
	BudgetOwner BudgetOwner `json:"budgetOwner"`
}

func loadCaptured(t *testing.T) []capturedError {
	t.Helper()
	raw, err := os.ReadFile("testdata/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []capturedError
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClassifyError_CapturedFromLiteLLM(t *testing.T) {
	captured := loadCaptured(t)
	versions := map[string]bool{}
	for _, c := range captured {
		versions[c.LiteLLM] = true
		t.Run(c.LiteLLM+" "+c.Case, func(t *testing.T) {
			got := ClassifyError(c.Status, []byte(c.Body))
			if got.Kind != c.Kind || got.BudgetOwner != c.BudgetOwner {
				t.Errorf("ClassifyError(%d) = %s/%q, want %s/%q", c.Status, got.Kind, got.BudgetOwner, c.Kind, c.BudgetOwner)
			}
		})
	}
	if !versions["1.102.4"] || !versions["1.105.1"] {
		t.Fatalf("fixture covers %v, want 1.102.4 and 1.105.1", versions)
	}
}

func TestClassifyError_BudgetWhicheverStatus(t *testing.T) {
	statuses := map[int]bool{}
	var body string
	for _, c := range loadCaptured(t) {
		if c.Kind == ErrorBudgetExceeded {
			statuses[c.Status] = true
			if c.LiteLLM == "1.105.1" && c.BudgetOwner == BudgetOfKey {
				body = c.Body
			}
		}
	}
	var seen []int
	for s := range statuses {
		seen = append(seen, s)
	}
	sort.Ints(seen)
	if len(seen) != 2 || seen[0] != 422 || seen[1] != 429 {
		t.Fatalf("budget statuses in the fixture = %v, want [422 429]", seen)
	}
	for _, status := range []int{0, 400, 402, 403, 422, 429, 500} {
		if got := ClassifyError(status, []byte(body)).Kind; got != ErrorBudgetExceeded {
			t.Errorf("HTTP %d: kind = %s, want budget_exceeded", status, got)
		}
	}
}

func TestClassifyError_Wording(t *testing.T) {
	tests := []struct {
		status int
		body   string
		kind   ErrorKind
		owner  BudgetOwner
	}{
		{400, `{"error":"Budget has been exceeded"}`, ErrorBudgetExceeded, ""},
		{429, `ExceededBudget: budget`, ErrorBudgetExceeded, ""},
		{422, `{"error":{"message":"ExceededBudget: Key over 1d budget. Spend=2.0, Budget=1.0","type":"budget_exceeded","param":null,"code":"422"}}`, ErrorBudgetExceeded, BudgetOfKey},
		{422, `{"error":{"message":"ExceededBudget: Team=t-1 over 30d budget. Spend=2.0, Budget=1.0","type":"budget_exceeded","param":null,"code":"422"}}`, ErrorBudgetExceeded, BudgetOfTeam},
		{422, `{"error":{"message":"Budget has been exceeded! User=u-1 in Team=t-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, ErrorBudgetExceeded, BudgetOfUser},
		{422, `{"error":{"message":"ExceededBudget: End User=e-1 over budget. Spend=2.0, Budget=1.0","type":"budget_exceeded"}}`, ErrorBudgetExceeded, BudgetOfEndUser},
		{422, `{"error":{"message":"Budget has been exceeded! Organization=o-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, ErrorBudgetExceeded, BudgetOfOrganization},
		{422, `{"error":{"message":"Budget has been exceeded! Model access group=g-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, ErrorBudgetExceeded, BudgetOfModelGroup},
		{418, `{"error":{"message":"over the limit","type":"budget_exceeded"}}`, ErrorBudgetExceeded, ""},
		{0, `Error: 422 {"error":{"message":"Budget has been exceeded! Key=ploeg-abc Current cost: 1.0, Max budget: 0.5","type":"budget_exceeded","param":null,"code":"422"}}`, ErrorBudgetExceeded, BudgetOfKey},
		{0, `BudgetExceededError: spend limit`, ErrorBudgetExceeded, ""},
		{402, ``, ErrorBudgetExceeded, ""},
		{401, `not json`, ErrorKeyRejected, ""},
		{403, `not json`, ErrorKeyRejected, ""},
		{404, `not json`, ErrorModelUnavailable, ""},
		{408, `not json`, ErrorTimeout, ""},
		{429, `not json`, ErrorRateLimited, ""},
		{400, `not json`, ErrorBadRequest, ""},
		{422, `{"detail":[{"loc":["body","model"],"msg":"field required"}]}`, ErrorBadRequest, ""},
		{500, `not json`, ErrorProviderUnavailable, ""},
		{503, `not json`, ErrorProviderUnavailable, ""},
		{504, `not json`, ErrorTimeout, ""},
		{500, `{"error":{"message":"litellm.Timeout: Request timed out","type":"timeout"}}`, ErrorTimeout, ""},
		{400, `{"error":{"message":"Unsupported parameter: timeout"}}`, ErrorBadRequest, ""},
		{0, `disk full`, ErrorUnknown, ""},
		{200, ``, ErrorUnknown, ""},
	}
	for _, tc := range tests {
		got := ClassifyError(tc.status, []byte(tc.body))
		if got.Kind != tc.kind || got.BudgetOwner != tc.owner {
			t.Errorf("ClassifyError(%d, %q) = %s/%q, want %s/%q", tc.status, tc.body, got.Kind, got.BudgetOwner, tc.kind, tc.owner)
		}
	}
}

func TestErrorKind_DescribeEveryKnownKind(t *testing.T) {
	for _, k := range []ErrorKind{ErrorBudgetExceeded, ErrorRateLimited, ErrorProviderQuota, ErrorKeyRejected, ErrorProviderAuth, ErrorModelUnavailable, ErrorContextTooLong, ErrorContentFiltered, ErrorTimeout, ErrorProviderUnavailable, ErrorBadRequest} {
		if k.Describe() == "" {
			t.Errorf("%s has no description", k)
		}
	}
	if ErrorUnknown.Describe() != "" {
		t.Error("unknown must not be described")
	}
}

func TestGatewayError_SharedBudgetSpentNamesWhoseBudget(t *testing.T) {
	want := map[string]string{
		"team budget spent": "the LiteLLM team budget for t2 is spent",
		"user budget spent": "the LiteLLM user budget for u-spent is spent",
	}
	seen := map[string]int{}
	for _, c := range loadCaptured(t) {
		got := ClassifyError(c.Status, []byte(c.Body))
		phrase, shared := want[c.Case]
		if got.SharedBudgetSpent() != shared {
			t.Errorf("%s %s (HTTP %d): SharedBudgetSpent = %v, want %v", c.LiteLLM, c.Case, c.Status, got.SharedBudgetSpent(), shared)
		}
		if shared {
			seen[c.Case]++
			if got.BudgetSpent() != phrase {
				t.Errorf("%s %s: BudgetSpent = %q, want %q", c.LiteLLM, c.Case, got.BudgetSpent(), phrase)
			}
		}
	}
	for c := range want {
		if seen[c] != 2 {
			t.Errorf("fixture has %d captures of %q, want one per LiteLLM version", seen[c], c)
		}
	}
}

func TestGatewayError_BudgetSpentPerOwner(t *testing.T) {
	tests := []struct {
		body   string
		shared bool
		phrase string
	}{
		{`{"error":{"message":"Budget has been exceeded! Key=key (sk-...rp3Q) Current cost: 1.0, Max budget: 0.01","type":"budget_exceeded"}}`, false, "the LiteLLM key budget is spent"},
		{`{"error":{"message":"ExceededBudget: Key over 1d budget. Spend=2.0, Budget=1.0","type":"budget_exceeded"}}`, false, "the LiteLLM key budget is spent"},
		{`{"error":{"message":"ExceededBudget: Team=t-1 over 30d budget. Spend=2.0, Budget=1.0","type":"budget_exceeded"}}`, true, "the LiteLLM team budget for t-1 is spent"},
		{`{"error":{"message":"Budget has been exceeded! User=u-1 in Team=t-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, true, "the LiteLLM user budget for u-1 is spent"},
		{`{"error":{"message":"ExceededBudget: End User=e-1 over budget. Spend=2.0, Budget=1.0","type":"budget_exceeded"}}`, true, "the LiteLLM end user budget for e-1 is spent"},
		{`{"error":{"message":"Budget has been exceeded! Organization=o-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, true, "the LiteLLM organization budget for o-1 is spent"},
		{`{"error":{"message":"Budget has been exceeded! Project=p-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, true, "the LiteLLM project budget for p-1 is spent"},
		{`{"error":{"message":"Budget has been exceeded! Tag=prod Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, true, "the LiteLLM tag budget for prod is spent"},
		{`{"error":{"message":"Budget has been exceeded! Model access group=g-1 Current cost: 2.0, Max budget: 1.0","type":"budget_exceeded"}}`, true, "the LiteLLM model access group budget for g-1 is spent"},
		{`Error: 422 {"error":{"message":"Budget has been exceeded! Team=t2 Current cost: 0.12, Max budget: 0.05","type":"budget_exceeded"}}`, true, "the LiteLLM team budget for t2 is spent"},
		{`{"error":"Budget has been exceeded"}`, false, "a LiteLLM budget is spent"},
		{`not json`, false, ""},
	}
	for _, tc := range tests {
		got := ClassifyError(0, []byte(tc.body))
		if got.SharedBudgetSpent() != tc.shared || got.BudgetSpent() != tc.phrase {
			t.Errorf("ClassifyError(%q) = shared %v %q, want shared %v %q", tc.body, got.SharedBudgetSpent(), got.BudgetSpent(), tc.shared, tc.phrase)
		}
	}
	if ClassifyError(402, nil).SharedBudgetSpent() {
		t.Error("a bare 402 names no owner, so it must not read as a shared budget")
	}
}
