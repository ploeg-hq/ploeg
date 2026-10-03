package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCompareListsCommits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"forgejo with commits", `{"total_commits":2,"commits":[{"sha":"a"},{"sha":"b"}]}`, true},
		{"forgejo empty", `{"total_commits":0,"commits":[]}`, false},
		{"forgejo commits before the count", `{"commits":[],"total_commits":0}`, false},
		{"gitlab with commits", `{"commit":{"id":"a"},"commits":[{"id":"a"}],"diffs":[{"diff":"x"}],"compare_same_ref":false}`, true},
		{"gitlab empty", `{"commit":null,"commits":[],"diffs":[],"compare_timeout":false,"compare_same_ref":false}`, false},
		{"commits null", `{"commits":null}`, false},
	} {
		got, err := CompareListsCommits(strings.NewReader(tc.body))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	for _, body := range []string{``, `[]`, `{"message":"not found"}`, `{"commits":{"a":1}}`, `{"total_commits":"x"}`} {
		if _, err := CompareListsCommits(strings.NewReader(body)); err == nil {
			t.Errorf("%q: no error", body)
		}
	}
}

func TestCompareListsCommits_StopsAtTheFirstCommit(t *testing.T) {
	body := io.MultiReader(strings.NewReader(`{"commits":[{"sha":"a"},`), failingReader{})
	got, err := CompareListsCommits(body)
	if err != nil || !got {
		t.Errorf("got %v, %v; the first entry answers without reading on", got, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read past the answer") }

type comparingForge struct {
	statuslessForge
	calls  int
	answer bool
}

func (f *comparingForge) IsAncestor(context.Context, string, string, string) (bool, error) {
	f.calls++
	return f.answer, nil
}

func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	f := &comparingForge{answer: false}
	if ok, err := IsAncestor(ctx, f, "o/r", "ABC", "abc"); !ok || err != nil || f.calls != 0 {
		t.Errorf("equal commits: %v, %v after %d reads; want true without a read", ok, err, f.calls)
	}
	if ok, err := IsAncestor(ctx, f, "o/r", "abc", "def"); ok || err != nil || f.calls != 1 {
		t.Errorf("compare: %v, %v after %d reads", ok, err, f.calls)
	}
	if _, err := IsAncestor(ctx, statuslessForge{}, "o/r", "abc", "def"); !errors.Is(err, ErrNoAncestry) {
		t.Errorf("a forge without compare: err = %v", err)
	}
	if _, err := IsAncestor(ctx, f, "o/r", "", "def"); err == nil {
		t.Error("an empty commit must be refused")
	}
}
