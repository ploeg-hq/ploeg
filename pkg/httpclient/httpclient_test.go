package httpclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func countingServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var opened atomic.Int64
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, &opened
}

func TestConcurrentCallsToOneHostReuseTheirConnections(t *testing.T) {
	const parallel = 10
	ts, opened := countingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	})
	client := New(5 * time.Second)
	wave := func() {
		var wg sync.WaitGroup
		for range parallel {
			wg.Go(func() {
				resp, err := client.Get(ts.URL)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			})
		}
		wg.Wait()
	}
	wave()
	first := opened.Load()
	wave()
	if again := opened.Load() - first; again != 0 {
		t.Errorf("a second wave of %d calls opened %d new connections after the first opened %d; the pool keeps too few idle per host",
			parallel, again, first)
	}
}
