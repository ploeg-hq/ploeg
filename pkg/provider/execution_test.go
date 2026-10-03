package provider

import (
	"context"
	"net/http"
	"testing"
)

func TestRepositoryLocatorPinsConfiguredForgeAndCoordinates(t *testing.T) {
	actual, err := RepositoryURL("https://forge.example/", "nested/owner", "repository")
	if err != nil || actual != "https://forge.example/nested/owner/repository.git" {
		t.Fatalf("registered repository: %s %v", actual, err)
	}
	actual, err = RepositoryURL("https://forge.example/", "owner", "nested/repository")
	if err != nil || actual != "https://forge.example/owner/nested/repository.git" {
		t.Fatalf("registered nested repository: %s %v", actual, err)
	}
	for _, tc := range []struct{ base, owner, repo string }{
		{"https://user:secret@forge.example", "owner", "repo"},
		{"https://forge.example", "../owner", "repo"},
		{"https://forge.example", "owner", "../repo"},
		{"https://forge.example", "owner", "repo?token=unsafe"},
		{"https://forge.example", "owner", "repo%2Fother"},
	} {
		if _, err := RepositoryURL(tc.base, tc.owner, tc.repo); err == nil {
			t.Fatalf("unsafe repository coordinate accepted: %+v", tc)
		}
	}
}

// providerFake implements the whole ForgeProvider SPI, so this file fails to
// compile the day a method is added and left unimplemented somewhere.
type providerFake struct{}

func (providerFake) Name() string                                                  { return "fake" }
func (providerFake) ParseWebhook(*http.Request) ([]ForgeEvent, error)              { return nil, nil }
func (providerFake) Comment(context.Context, string, int, string) error            { return nil }
func (providerFake) Comments(context.Context, string, int) ([]Comment, error)      { return nil, nil }
func (providerFake) EditComment(context.Context, string, int, int64, string) error { return nil }
func (providerFake) PullRequestState(context.Context, string, int) (PullRequestState, error) {
	return PullRequestOpen, nil
}
func (providerFake) PullRequestFacts(context.Context, string, int) (PullRequestFacts, error) {
	return PullRequestFacts{State: PullRequestOpen}, nil
}

var _ ForgeProvider = providerFake{}

func TestForgeProvider_CommentMethodsInSPI(t *testing.T) {
	var fp ForgeProvider = providerFake{}
	if got, err := fp.Comments(context.Background(), "o/r", 1); err != nil || got != nil {
		t.Fatalf("Comments = %v, %v", got, err)
	}
	if err := fp.EditComment(context.Background(), "o/r", 1, 2, "body"); err != nil {
		t.Fatalf("EditComment: %v", err)
	}
}
