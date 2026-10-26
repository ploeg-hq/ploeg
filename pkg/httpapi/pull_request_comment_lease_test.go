package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
)

func TestPullRequestComment_HoldsNoConnectionDuringForgeCalls(t *testing.T) {
	s, forge, token, id := keyedCommentServer(t, "forgejo", true)
	ctx := context.Background()
	dsn := fmt.Sprintf("postgresql://postgres:postgres@localhost:%d/postgres?sslmode=disable", testPort)
	single, err := store.New(ctx, dsn, store.PoolConfig{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	s.Store = single
	forge.listing = make(chan struct{}, 1)
	forge.release = make(chan struct{})

	done := make(chan int, 1)
	go func() {
		w := operatorDo(t, s, token, "PUT", fmt.Sprintf("/api/v1/operator/work-items/%d/pull-request-comments/summary", id),
			`{"markdown":"slow forge"}`, actorHeaders())
		done <- w.Code
	}()
	select {
	case <-forge.listing:
	case <-time.After(10 * time.Second):
		t.Fatal("the forge was never asked for the comments")
	}

	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, queryErr := single.CommentPullRequest(queryCtx, id, nil, 0)
	cancel()
	close(forge.release)
	if queryErr != nil {
		t.Errorf("a one-connection pool could not serve a query while the forge call was in flight: %v", queryErr)
	}
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("put = %d, want 200", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the comment write did not finish on a one-connection pool")
	}
	if bodies := forge.bodies(); len(bodies) != 1 {
		t.Errorf("forge comments = %q, want one", bodies)
	}
}
