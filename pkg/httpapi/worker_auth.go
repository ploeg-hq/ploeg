package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ploeg-hq/ploeg/pkg/store"
	"github.com/ploeg-hq/ploeg/pkg/txntoken"
)

type WorkerBootstrap struct {
	Token string `json:"token"`
	Team  string `json:"team"`
	Role  string `json:"role"`
}

type WorkerSecurity struct {
	AllowLegacy bool
	// RetireHMAC stops ploegd issuing and accepting the HMAC worker
	// capability, so a Run is controlled with its transaction token alone
	// (ADR-0156, PLOEG_WORKER_HMAC_CAPABILITY=off).
	RetireHMAC      bool
	bootstraps      []WorkerBootstrap
	signingKey      []byte
	mu              sync.Mutex
	rejectionWindow time.Time
	rejections      int
}

type workerCapability struct {
	Audience string `json:"aud"`
	RunToken string `json:"run"`
	WorkerID string `json:"worker"`
	Team     string `json:"team"`
	Role     string `json:"role"`
	Expires  int64  `json:"exp"`
}

func NewWorkerSecurity(bootstrapJSON, signingKey string, allowLegacy bool) (*WorkerSecurity, error) {
	a := &WorkerSecurity{AllowLegacy: allowLegacy, signingKey: []byte(signingKey)}
	if allowLegacy {
		if signingKey != "" || bootstrapJSON != "" {
			return nil, fmt.Errorf("legacy worker mode cannot carry managed credentials")
		}
		return a, nil
	}
	if len(signingKey) < 32 {
		return nil, fmt.Errorf("worker capability signing key must contain at least 32 bytes")
	}
	return a, a.readBootstraps(bootstrapJSON)
}

// NewTransactionTokenWorkerSecurity is managed worker security without the
// HMAC capability (ADR-0156): a claim answers a transaction token only, and a
// Run route accepts nothing else, so no worker signing key is needed.
func NewTransactionTokenWorkerSecurity(bootstrapJSON string) (*WorkerSecurity, error) {
	a := &WorkerSecurity{RetireHMAC: true}
	return a, a.readBootstraps(bootstrapJSON)
}

func (a *WorkerSecurity) readBootstraps(bootstrapJSON string) error {
	if err := json.Unmarshal([]byte(bootstrapJSON), &a.bootstraps); err != nil {
		return fmt.Errorf("invalid worker bootstrap registry")
	}
	for i, b := range a.bootstraps {
		if b.Team == "" || len(b.Token) < 32 {
			return fmt.Errorf("invalid worker bootstrap scope")
		}
		for j := 0; j < i; j++ {
			if a.bootstraps[j].Token == b.Token {
				return fmt.Errorf("worker bootstrap credentials must be unique per scope")
			}
		}
	}
	return nil
}

func (a *WorkerSecurity) bootstrap(token string) (WorkerBootstrap, bool) {
	got := sha256.Sum256([]byte(token))
	for _, b := range a.bootstraps {
		want := sha256.Sum256([]byte(b.Token))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return b, true
		}
	}
	return WorkerBootstrap{}, false
}

func (a *WorkerSecurity) sign(c workerCapability) string {
	body, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, a.signingKey)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *WorkerSecurity) verify(token string) (workerCapability, bool) {
	var c workerCapability
	if a.RetireHMAC || len(a.signingKey) == 0 || len(token) > 4096 {
		return c, false
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return c, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return c, false
	}
	mac := hmac.New(sha256.New, a.signingKey)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, false
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(body, &c) != nil {
		return c, false
	}
	return c, c.Audience == "ploeg-worker-run" && c.Expires > time.Now().Unix() && c.WorkerID != ""
}

func (s *Server) WorkerHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/api/v1/claim" && !strings.HasPrefix(path, "/api/v1/runs/") && !strings.HasPrefix(path, "/api/v1/queue/") {
			next.ServeHTTP(w, r)
			return
		}
		a := s.WorkerSecurity
		if a != nil && a.AllowLegacy && s.LLMControl == nil {
			next.ServeHTTP(w, r)
			return
		}
		if a == nil {
			http.Error(w, "worker authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || token == "" {
			s.rejectWorker(w, r)
			return
		}
		if path == "/api/v1/claim" {
			b, ok := a.bootstrap(token)
			worker := r.Header.Get("X-Ploeg-Worker-ID")
			if !ok || worker == "" || len(worker) > 128 || r.Method != http.MethodPost {
				s.rejectWorker(w, r)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			var req claimRequest
			if err != nil || json.Unmarshal(body, &req) != nil || req.Team != b.Team || req.Role != b.Role {
				s.rejectWorker(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			capture := &workerResponse{header: make(http.Header)}
			next.ServeHTTP(capture, r)
			if capture.status == http.StatusOK {
				var claimed claimResponse
				var result map[string]any
				if json.Unmarshal(capture.body.Bytes(), &claimed) != nil || json.Unmarshal(capture.body.Bytes(), &result) != nil || s.LLMControl == nil {
					http.Error(w, "managed claim unavailable", http.StatusServiceUnavailable)
					return
				}
				if err := s.LLMControl.Reserve(r.Context(), claimed.RunToken); err != nil {
					http.Error(w, "inference policy could not reserve claim", http.StatusServiceUnavailable)
					return
				}
				if !a.RetireHMAC {
					result["controlToken"] = a.sign(workerCapability{Audience: "ploeg-worker-run", RunToken: claimed.RunToken, WorkerID: worker, Team: b.Team, Role: b.Role, Expires: time.Now().Add(24 * time.Hour).Unix()})
				}
				if s.TransactionTokens != nil {
					token, _, err := s.issueTransactionToken(r.Context(), claimed.RunToken, worker)
					if err != nil {
						s.Log.Error("claimed run got no transaction token", "team", b.Team, "role", b.Role, "err", err)
						http.Error(w, "transaction token unavailable", http.StatusServiceUnavailable)
						return
					}
					result["transactionToken"] = token
				}
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusOK, result)
				return
			}
			capture.copyTo(w)
			return
		}
		if strings.HasPrefix(path, "/api/v1/queue/") {
			s.rejectWorker(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/api/v1/runs/"), "/")
		worker := r.Header.Get("X-Ploeg-Worker-ID")
		if len(parts) < 2 || worker == "" {
			s.rejectWorker(w, r)
			return
		}
		run, err := s.Store.RunControl(r.Context(), parts[0])
		if err != nil {
			s.rejectWorker(w, r)
			return
		}
		ctx := r.Context()
		if txntoken.LooksSigned(token) {
			actor, ok := s.verifyTransactionToken(token, worker, run)
			if !ok {
				s.rejectWorker(w, r)
				return
			}
			ctx = store.WithRunActor(ctx, actor)
		} else {
			c, ok := a.verify(token)
			if !ok || c.RunToken != parts[0] || c.WorkerID != worker || run.Team != c.Team || run.Role != c.Role {
				s.rejectWorker(w, r)
				return
			}
		}
		terminalReplay := len(parts) == 2 && parts[1] == "outcome" && run.State == "finished"
		if !terminalReplay && (run.State != "running" || !run.Deadline.After(time.Now())) {
			s.rejectWorker(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withRunWorker(ctx, worker)))
	})
}

func (s *Server) rejectWorker(w http.ResponseWriter, r *http.Request) {
	a := s.WorkerSecurity
	a.mu.Lock()
	if time.Since(a.rejectionWindow) >= time.Minute {
		a.rejectionWindow = time.Now()
		a.rejections = 0
	}
	a.rejections++
	limited := a.rejections > 30
	a.mu.Unlock()
	if limited {
		http.Error(w, "worker request rate limited", http.StatusTooManyRequests)
		return
	}
	if s.Log != nil {
		s.Log.Warn("worker request rejected")
	}
	if s.Store != nil {
		_ = s.Store.RecordWorkerRejection(r.Context())
	}
	http.Error(w, "worker request unauthorized", http.StatusUnauthorized)
}

type workerResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *workerResponse) Header() http.Header { return w.header }
func (w *workerResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *workerResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}
func (w *workerResponse) copyTo(dst http.ResponseWriter) {
	for k, v := range w.header {
		dst.Header()[k] = v
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(w.body.Bytes())
}
