package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func jsonWithPadding(prefix string, padding int, suffix string) []byte {
	return []byte(prefix + strings.Repeat("a", padding) + suffix)
}

func legacyRunServer() *Server {
	return &Server{Store: testStore, LeaseTTL: time.Minute, Log: slog.New(slog.DiscardHandler),
		WorkerSecurity: &WorkerSecurity{AllowLegacy: true}}
}

func postRawRunBody(s *Server, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRunRoutesRefuseOversizedBodiesWith413(t *testing.T) {
	s := legacyRunServer()
	for _, tc := range []struct {
		name, path string
		body       []byte
	}{
		{"claim", "/api/v1/claim", jsonWithPadding(`{"team":"`, int(claimBodyLimit), `"}`)},
		{"checkpoint", "/api/v1/runs/no-such-run/checkpoint", jsonWithPadding(`{"phase":"started","branch":"`, int(checkpointBodyLimit), `"}`)},
		{"outcome", "/api/v1/runs/no-such-run/outcome", jsonWithPadding(`{"outcome":"no_change_needed","summary":"`, int(outcomeBodyLimit), `"}`)},
		{"transcript", "/api/v1/runs/no-such-run/transcript", jsonWithPadding(`{"entries":[{"text":"`, int(maxTranscriptRequestBytes), `"}]}`)},
		{"board", "/api/v1/runs/no-such-run/board", jsonWithPadding(`{"checkpoint":"refresh","forgeHead":"`, int(boardRequestLimit), `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postRawRunBody(s, tc.path, tc.body)
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("a %d-byte %s body answered %d %q, want 413", len(tc.body), tc.name, w.Code, w.Body.String())
			}
		})
	}
}

func TestRunRoutesAcceptTheLargestContractPayloadSize(t *testing.T) {
	reset(t)
	s := legacyRunServer()
	for _, tc := range []struct {
		name, path string
		body       []byte
	}{
		{"checkpoint", "/api/v1/runs/no-such-run/checkpoint", jsonWithPadding(`{"phase":"started","branch":"`, int(checkpointBodyLimit)-64, `"}`)},
		{"outcome", "/api/v1/runs/no-such-run/outcome", jsonWithPadding(`{"outcome":"no_change_needed","summary":"`, int(outcomeBodyLimit)-64, `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := postRawRunBody(s, tc.path, tc.body); w.Code == http.StatusRequestEntityTooLarge || w.Code == http.StatusBadRequest {
				t.Fatalf("a %d-byte %s body under the limit answered %d %q", len(tc.body), tc.name, w.Code, w.Body.String())
			}
		})
	}
}

type tricklingReader struct {
	data  []byte
	chunk int
	pause time.Duration
}

func (r *tricklingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	n := min(r.chunk, len(p), len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestContextUploadOutlivesTheServerReadAndWriteTimeouts(t *testing.T) {
	reset(t)
	s, token := contextServer(t, []string{"silver"}, true)
	id := ingestFor(t, "9101", "silver")
	bundle := zipBundle(t, map[string]string{"design/a.md": "# A\n"})

	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.ReadTimeout = 300 * time.Millisecond
	ts.Config.WriteTimeout = 300 * time.Millisecond
	ts.Start()
	defer ts.Close()

	chunk := len(bundle)/8 + 1
	body := &tricklingReader{data: bundle, chunk: chunk, pause: 120 * time.Millisecond}
	path := ts.URL + "/api/v1/operator/work-items/" + strconv.FormatInt(id, 10) + "/context?" + url.Values{"name": {"design.zip"}}.Encode()
	req, err := http.NewRequest(http.MethodPost, path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(bundle))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Ploeg-Actor", "console")
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("an upload that took %s past a 300ms server budget failed: %v", time.Since(start), err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("slow upload answered %d %s", resp.StatusCode, got)
	}
}
