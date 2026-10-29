package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func provenRig(t *testing.T) *personRig {
	t.Helper()
	rig := newPersonRig(t)
	rig.server.OperatorConfig.Attribution = AttributionProven
	return rig
}

func TestProvenModeRefusesAChangeThatOnlyAssertsItsPerson(t *testing.T) {
	rig := provenRig(t)
	for _, tc := range []struct {
		method  string
		headers map[string]string
	}{
		{"POST", map[string]string{"X-Ploeg-Actor": "alice"}},
		{"POST", map[string]string{"X-Ploeg-Actor": "alice", "X-Ploeg-Acting-User": "admin"}},
		{"POST", nil},
		{"PUT", map[string]string{"X-Ploeg-Actor": "alice"}},
		{"PATCH", map[string]string{"X-Ploeg-Actor": "alice"}},
		{"DELETE", map[string]string{"X-Ploeg-Actor": "alice"}},
	} {
		w := rig.do(t, tc.method, rig.token, tc.headers)
		if w.Code != http.StatusForbidden || rig.reached {
			t.Fatalf("%s %v: %d %s, reached=%v; want 403 before the route", tc.method, tc.headers, w.Code, w.Body, rig.reached)
		}
		if code := operatorErrorCode(t, w); code != "person_token_required" {
			t.Fatalf("%s %v: code %q, want person_token_required", tc.method, tc.headers, code)
		}
	}
}

func TestProvenModeAcceptsAChangeWithAnExchangedPersonToken(t *testing.T) {
	rig := provenRig(t)
	token := rig.issuer.sign(rig.issuer.claims())
	for name, tc := range map[string]struct {
		bearer  string
		headers map[string]string
	}{
		"as the bearer":                         {token, nil},
		"next to the consumer's own credential": {rig.token, map[string]string{PersonTokenHeader: token, "X-Ploeg-Actor": rig.issuer.personID()}},
	} {
		w := rig.do(t, "POST", tc.bearer, tc.headers)
		if w.Code != http.StatusNoContent || rig.seen == nil {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if rig.seen.Proof != ProofProven || rig.seen.Actor != rig.issuer.personID() {
			t.Fatalf("%s: attribution %+v, want the proven person", name, *rig.seen)
		}
		if warning := w.Header().Get("Warning"); warning != "" {
			t.Fatalf("%s: a proven change carries Warning %q", name, warning)
		}
	}
}

func TestProvenModeKeepsReadsOpenToAssertedAttribution(t *testing.T) {
	rig := provenRig(t)
	for _, method := range []string{"GET", "HEAD"} {
		if w := rig.do(t, method, rig.token, map[string]string{"X-Ploeg-Actor": "alice"}); w.Code != http.StatusNoContent || !rig.reached {
			t.Fatalf("%s with only the header: %d %s", method, w.Code, w.Body)
		}
		if w := rig.do(t, method, rig.token, nil); w.Code != http.StatusNoContent || !rig.reached {
			t.Fatalf("%s with the consumer's credential alone: %d %s", method, w.Code, w.Body)
		}
	}
}

func TestProvenModeStillAnswersAnInvalidPersonTokenFirst(t *testing.T) {
	rig := provenRig(t)
	c := rig.issuer.claims()
	c["aud"] = "someone-else"
	if w := rig.do(t, "POST", rig.issuer.sign(c), nil); w.Code != http.StatusUnauthorized || rig.reached {
		t.Fatalf("an invalid person token under proven: %d %s", w.Code, w.Body)
	}
	if w := rig.do(t, "POST", rig.issuer.sign(rig.issuer.claims()), map[string]string{"X-Ploeg-Actor": "mallory"}); w.Code != http.StatusForbidden || operatorErrorCode(t, w) != "actor_conflict" {
		t.Fatalf("a conflicting actor under proven: %d %s", w.Code, w.Body)
	}
}

func TestAssertedModeAcceptsAHeaderOnlyChangeWithAWarning(t *testing.T) {
	rig := newPersonRig(t)
	w := rig.do(t, "POST", rig.token, map[string]string{"X-Ploeg-Actor": "alice"})
	if w.Code != http.StatusNoContent || rig.seen == nil || rig.seen.Proof != ProofAsserted {
		t.Fatalf("header-only change under asserted: %d %s %+v", w.Code, w.Body, rig.seen)
	}
	warning := w.Header().Get("Warning")
	if !strings.HasPrefix(warning, `299 ploegd "`) || !strings.Contains(warning, "PLOEG_OPERATOR_ATTRIBUTION=proven") {
		t.Fatalf("Warning = %q, want an RFC 9111 299 warning naming the proven mode", warning)
	}
	if w := rig.do(t, "GET", rig.token, map[string]string{"X-Ploeg-Actor": "alice"}); w.Header().Get("Warning") != "" {
		t.Fatalf("a read carries Warning %q", w.Header().Get("Warning"))
	}
}

func TestParseAttributionMode(t *testing.T) {
	for raw, want := range map[string]AttributionMode{"": AttributionAsserted, " asserted ": AttributionAsserted, "proven": AttributionProven} {
		if got, err := ParseAttributionMode(raw); err != nil || got != want {
			t.Errorf("ParseAttributionMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"Proven", "trusted", "off"} {
		if _, err := ParseAttributionMode(raw); err == nil {
			t.Errorf("ParseAttributionMode(%q) accepted an unknown mode", raw)
		}
	}
}

func TestARefusedChangeWritesTheConsumerAndReasonToTheAudit(t *testing.T) {
	f := newRequeueFixture(t)
	f.server.OperatorConfig.Consumers[0].clientIDs = []string{personClient}
	is := newPersonIssuer(t)
	tokens, err := NewPersonTokens(PersonTokenConfig{Issuer: is.url, AllowLoopbackHTTP: true, Audience: personAudience, Now: func() time.Time { return personNow }}, f.server.OperatorConfig.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	f.server.OperatorConfig.PersonTokens = tokens
	f.server.OperatorConfig.Attribution = AttributionProven
	live := f.item(t, "5791")
	note := map[string]any{"commandId": "note-asserted", "text": "Asserted."}
	w := attributedRequest(f.server, "POST", notesPath(live), f.token, map[string][]string{"X-Ploeg-Actor": {"alice"}}, note)
	if w.Code != http.StatusForbidden || operatorErrorCode(t, w) != "person_token_required" {
		t.Fatalf("header-only note under proven: %d %s", w.Code, w.Body)
	}
	var actor string
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT actor, detail FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`,
		store.AuditOperatorAttributionRefused).Scan(&actor, &raw); err != nil {
		t.Fatalf("refusal audit row: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"consumer": "workbench", "reason": "person_token_required", "method": "POST", "path": notesPath(live), "assertedActor": "alice"}
	if actor != "operator:workbench" || len(detail) != len(want) {
		t.Fatalf("refusal row = %s %v, want operator:workbench %v", actor, detail, want)
	}
	for k, v := range want {
		if detail[k] != v {
			t.Fatalf("refusal detail %s = %v, want %v (%v)", k, detail[k], v, detail)
		}
	}
	var notes int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE work_item_id = $1 AND action = $2`, live, store.AuditOperatorNoteAdded).Scan(&notes); err != nil || notes != 0 {
		t.Fatalf("the refused note was recorded: %d %v", notes, err)
	}
	w = attributedRequest(f.server, "POST", notesPath(live), is.sign(is.claims()), nil, map[string]any{"commandId": "note-proven", "text": "Proven."})
	if w.Code != http.StatusCreated {
		t.Fatalf("the same note with a person token: %d %s", w.Code, w.Body)
	}
	if got := auditAttribution(t, live, store.AuditOperatorNoteAdded); got.Proof != ProofProven || got.Actor != is.personID() {
		t.Fatalf("attribution = %+v, want the proven person", got)
	}
}
