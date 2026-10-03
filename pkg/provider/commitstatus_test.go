package provider

import (
	"context"
	"net/http"
	"testing"
)

func TestCombineCommitStates(t *testing.T) {
	for _, tc := range []struct {
		in   []CommitState
		want CommitState
		ok   bool
	}{
		{nil, "", false},
		{[]CommitState{CommitSuccess, CommitSuccess}, CommitSuccess, true},
		{[]CommitState{CommitSuccess, CommitPending}, CommitPending, true},
		{[]CommitState{CommitPending, CommitError}, CommitError, true},
		{[]CommitState{CommitError, CommitFailure, CommitPending}, CommitFailure, true},
	} {
		got, ok := CombineCommitStates(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CombineCommitStates(%v) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

type statuslessForge struct{}

func (statuslessForge) Name() string                                     { return "plain" }
func (statuslessForge) ParseWebhook(*http.Request) ([]ForgeEvent, error) { return nil, nil }
func (statuslessForge) Comment(context.Context, string, int, string) error {
	return nil
}
func (statuslessForge) Comments(context.Context, string, int) ([]Comment, error) { return nil, nil }
func (statuslessForge) EditComment(context.Context, string, int, int64, string) error {
	return nil
}
func (statuslessForge) PullRequestState(context.Context, string, int) (PullRequestState, error) {
	return PullRequestOpen, nil
}
func (statuslessForge) PullRequestFacts(context.Context, string, int) (PullRequestFacts, error) {
	return PullRequestFacts{}, nil
}

func TestReadCommitStatus_AForgeWithoutStatusesLeavesCIUnknown(t *testing.T) {
	_, ok, err := ReadCommitStatus(context.Background(), statuslessForge{}, "o/r", "abc")
	if ok || err != nil {
		t.Fatalf("ok = %v, err = %v; want unknown without error", ok, err)
	}
}
