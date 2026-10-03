package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

const webhookLimit = 1 << 20

func webhookSink() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookLimit))
		if err != nil {
			http.Error(w, "webhook rejected", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, "%d", len(body))
	})
}

func serve(t *testing.T, handler http.Handler, timeouts httpTimeouts) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", handler, timeouts)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestDefaultHTTPTimeoutsBoundEveryRead(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler(), defaultHTTPTimeouts)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("server leaves a read unbounded: header=%s read=%s idle=%s",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.ReadTimeout < srv.ReadHeaderTimeout {
		t.Errorf("ReadTimeout %s is shorter than ReadHeaderTimeout %s", srv.ReadTimeout, srv.ReadHeaderTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout %s also bounds handler time; deploy checks and webhooks call out to forges and trackers", srv.WriteTimeout)
	}
}

func TestSlowBodyClientIsDisconnectedWithinTheReadBudget(t *testing.T) {
	budget := 500 * time.Millisecond
	addr := serve(t, webhookSink(), httpTimeouts{readHeader: budget, read: budget, idle: budget})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := fmt.Fprint(conn, "POST /webhooks/tracker/vikunja HTTP/1.1\r\nHost: ploegd\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if _, err := conn.Write([]byte("{")); err != nil {
					return
				}
			}
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(budget + 3*time.Second))
	_, err = io.Copy(io.Discard, conn)
	elapsed := time.Since(start)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a client trickling its body still held the connection after %s; the read budget is %s", elapsed, budget)
	}
	if elapsed > budget+time.Second {
		t.Errorf("connection closed after %s; the read budget is %s", elapsed, budget)
	}
}

func TestMaximumWebhookAtNormalSpeedSucceeds(t *testing.T) {
	addr := serve(t, webhookSink(), defaultHTTPTimeouts)
	payload := bytes.Repeat([]byte("a"), webhookLimit)

	resp, err := http.Post("http://"+addr+"/webhooks/tracker/vikunja", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != fmt.Sprint(webhookLimit) {
		t.Fatalf("1 MiB webhook: status %d, body %q", resp.StatusCode, got)
	}
}
