package work

import "testing"

func TestEveryFailureReasonIsExplained(t *testing.T) {
	for _, f := range FailureReasons() {
		e, ok := f.Explain()
		if !ok {
			t.Errorf("%q has no explanation; a consumer would show a bare enum value", f)
			continue
		}
		if e.Reason != string(f) || e.Title == "" || e.Action == "" {
			t.Errorf("%q explanation is incomplete: %+v", f, e)
		}
		if e.Cause != CauseTicket && e.Cause != CauseSystem {
			t.Errorf("%q cause = %q, want ticket or system", f, e.Cause)
		}
	}
	if _, ok := FailureReason("").Explain(); ok {
		t.Error("the empty reason is not a failure and must not be explained")
	}
}

func TestInfraAndConfigurationFailuresAreTheSystems(t *testing.T) {
	system := map[FailureReason]bool{FailureSharedBudgetSpent: true, FailureHarnessConfig: true}
	for _, f := range FailureReasons() {
		e, _ := f.Explain()
		want := CauseTicket
		if f.IsInfra() || system[f] {
			want = CauseSystem
		}
		if e.Cause != want {
			t.Errorf("%q cause = %q, want %q", f, e.Cause, want)
		}
	}
}

func TestFailureCloseReasonsAreExplained(t *testing.T) {
	for reason, cause := range map[string]FailureCause{
		CloseWriterFailed: CauseTicket, CloseWriterKilled: CauseSystem, CloseRunCapReached: CauseTicket,
		CloseHarnessConfig: CauseSystem, CloseCredentialLeak: CauseTicket, CloseSharedBudgetSpent: CauseSystem,
	} {
		e, ok := ExplainClose(reason)
		if !ok || e.Cause != cause || e.Reason != reason || e.Title == "" || e.Action == "" {
			t.Errorf("ExplainClose(%q) = %+v, %v; want cause %q with a title and an action", reason, e, ok, cause)
		}
	}
	if _, ok := ExplainClose("review_approved"); ok {
		t.Error("a close that is not a failure must not be explained as one")
	}
}
