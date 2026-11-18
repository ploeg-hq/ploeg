// Package mcpstdio serves an MCP server that speaks only stdio over the
// Streamable HTTP transport, as the sandboxed MCP adapter of ADR-0199
// section 7: one child process per MCP session, so two Runs never share a
// server's state, each ended on DELETE, when idle, or when the bridge stops.
package mcpstdio

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// Limits of one bridge.
const (
	MaxMessage   = 1 << 20
	DefaultIdle  = 10 * time.Minute
	DefaultLimit = 16
	callTimeout  = 2 * time.Minute
)

// Bridge serves POST and DELETE on its handler for one stdio command.
type Bridge struct {
	Command  []string
	Env      []string
	Idle     time.Duration
	Sessions int
	Log      *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	waiting map[string]chan json.RawMessage
	last    time.Time
	done    chan struct{}
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
}

func (b *Bridge) idle() time.Duration {
	if b.Idle <= 0 {
		return DefaultIdle
	}
	return b.Idle
}

func (b *Bridge) limit() int {
	if b.Sessions <= 0 {
		return DefaultLimit
	}
	return b.Sessions
}

func (b *Bridge) log() *slog.Logger {
	if b.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return b.Log
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		b.post(w, r)
	case http.MethodDelete:
		b.end(r.Header.Get("Mcp-Session-Id"))
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *Bridge) post(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxMessage+1))
	if err != nil || len(body) > MaxMessage {
		http.Error(w, "a message is at most 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	var m message
	if json.Unmarshal(body, &m) != nil || m.JSONRPC != "2.0" {
		http.Error(w, "one JSON-RPC 2.0 message per request", http.StatusBadRequest)
		return
	}
	id := r.Header.Get("Mcp-Session-Id")
	var s *session
	if m.Method == "initialize" {
		if id, s, err = b.start(); err != nil {
			b.log().Warn("mcp adapter session not started", "err", err)
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Mcp-Session-Id", id)
	} else if s = b.session(id); s == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if len(m.ID) == 0 {
		if err := s.write(body); err != nil {
			http.Error(w, "session ended", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := s.expect(string(m.ID))
	if err := s.write(body); err != nil {
		http.Error(w, "session ended", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	select {
	case raw := <-reply:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	case <-s.done:
		http.Error(w, "the server exited", http.StatusBadGateway)
	case <-ctx.Done():
		s.forget(string(m.ID))
		http.Error(w, "the server did not answer", http.StatusGatewayTimeout)
	}
}

func (b *Bridge) session(id string) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	if s != nil {
		s.last = time.Now()
	}
	return s
}

func (b *Bridge) start() (string, *session, error) {
	if len(b.Command) == 0 {
		return "", nil, errors.New("no command")
	}
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = map[string]*session{}
	}
	if len(b.sessions) >= b.limit() {
		b.mu.Unlock()
		return "", nil, errors.New("session limit reached")
	}
	b.mu.Unlock()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	id := hex.EncodeToString(raw)
	cmd := exec.Command(b.Command[0], b.Command[1:]...)
	cmd.Env = b.Env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	s := &session{cmd: cmd, stdin: stdin, waiting: map[string]chan json.RawMessage{}, last: time.Now(), done: make(chan struct{})}
	go s.read(stdout)
	go func() {
		_ = cmd.Wait()
		close(s.done)
		b.mu.Lock()
		delete(b.sessions, id)
		b.mu.Unlock()
	}()
	b.mu.Lock()
	b.sessions[id] = s
	b.mu.Unlock()
	return id, s, nil
}

func (s *session) write(line []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(append([]byte{}, line...), '\n')); err != nil {
		return err
	}
	return nil
}

func (s *session) expect(id string) chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.waiting[id] = ch
	s.mu.Unlock()
	return ch
}

func (s *session) forget(id string) {
	s.mu.Lock()
	delete(s.waiting, id)
	s.mu.Unlock()
}

func (s *session) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), MaxMessage)
	for sc.Scan() {
		raw := append(json.RawMessage{}, sc.Bytes()...)
		var m message
		if json.Unmarshal(raw, &m) != nil || len(m.ID) == 0 {
			continue
		}
		if m.Method != "" {
			refusal, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "the client answers no server request"}})
			_ = s.write(refusal)
			continue
		}
		s.mu.Lock()
		ch, ok := s.waiting[string(m.ID)]
		delete(s.waiting, string(m.ID))
		s.mu.Unlock()
		if ok {
			ch <- raw
		}
	}
}

func (b *Bridge) end(id string) {
	b.mu.Lock()
	s := b.sessions[id]
	delete(b.sessions, id)
	b.mu.Unlock()
	if s != nil {
		_ = s.stdin.Close()
		_ = s.cmd.Process.Kill()
	}
}

// Reap ends idle sessions until ctx ends, then ends every session.
func (b *Bridge) Reap(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			b.mu.Lock()
			ids := make([]string, 0, len(b.sessions))
			for id := range b.sessions {
				ids = append(ids, id)
			}
			b.mu.Unlock()
			for _, id := range ids {
				b.end(id)
			}
			return
		case now := <-t.C:
			b.ReapIdle(now)
		}
	}
}

// ReapIdle ends every session idle since before now minus the idle limit.
func (b *Bridge) ReapIdle(now time.Time) {
	b.mu.Lock()
	var idle []string
	for id, s := range b.sessions {
		if now.Sub(s.last) > b.idle() {
			idle = append(idle, id)
		}
	}
	b.mu.Unlock()
	for _, id := range idle {
		b.end(id)
	}
}
