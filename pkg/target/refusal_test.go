package target

import (
	"errors"
	"strings"
	"testing"
)

func TestRouteRefusalCarriesACodeAndTheLabelsTheBoardAllows(t *testing.T) {
	r := registryResolver(t)
	cases := []struct {
		name    string
		req     Request
		code    string
		allowed []string
	}{
		{"no label on a hint-required board", Request{Scope: "7", Labels: []string{"do-next"}, LabelsRead: true}, RefusalLabelMissing, []string{"repo/app", "repo/homelab-cluster"}},
		{"a registered target the board does not allow", Request{Scope: "10", Labels: []string{"repo/erfbeeld"}, LabelsRead: true}, RefusalLabelNotAllowed, []string{"repo/app", "repo/homelab-cluster"}},
		{"another repository on a board without allow", Request{Scope: "4", Labels: []string{"repo/homelab-cluster"}, LabelsRead: true}, RefusalLabelNotAllowed, []string{"repo/erfbeeld", "repo/nuala-nalatenschap"}},
		{"unreadable labels on a board that routes by label", Request{Scope: "10"}, RefusalLabelsUnread, []string{"repo/app", "repo/homelab-cluster"}},
		{"two different repo labels", Request{Scope: "10", Labels: []string{"repo/homelab-cluster", "repo/app"}, LabelsRead: true}, RefusalMultipleLabels, []string{"repo/app", "repo/homelab-cluster"}},
		{"an unregistered repo label", Request{Scope: "10", Labels: []string{"repo/ploeg"}, LabelsRead: true}, RefusalLabelUnregistered, []string{"repo/app", "repo/homelab-cluster"}},
		{"a label on a board no rule covers", Request{Scope: "99", Labels: []string{"repo/app"}, LabelsRead: true}, RefusalNoBoardRule, []string{}},
		{"two labels on a board no rule covers", Request{Scope: "99", Labels: []string{"repo/app", "repo/erfbeeld"}, LabelsRead: true}, RefusalMultipleLabels, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := r.Route(c.req)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Route(%+v) err = %v, want a refusal", c.req, err)
			}
			if refusal.Code != c.code {
				t.Errorf("code = %q, want %q", refusal.Code, c.code)
			}
			if refusal.Allowed == nil || strings.Join(refusal.Allowed, ",") != strings.Join(c.allowed, ",") {
				t.Errorf("allowed = %#v, want %#v", refusal.Allowed, c.allowed)
			}
		})
	}
}

func TestRouteRefusalSentenceListsTheSameAllowedLabels(t *testing.T) {
	r := registryResolver(t)
	_, _, err := r.Route(Request{Scope: "7", LabelsRead: true})
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if want := "add one of: " + strings.Join(refusal.Allowed, ", "); !strings.HasSuffix(refusal.Reason, want) {
		t.Errorf("reason = %q, want it to end with %q", refusal.Reason, want)
	}
}
