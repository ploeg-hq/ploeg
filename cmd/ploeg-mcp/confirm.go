package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	confirmationKeyEnv      = "PLOEG_MCP_CONFIRMATION_KEY"
	minConfirmationKeyBytes = 32
	maxSpentConfirmations   = 100_000
	confirmationMACContext  = "ploeg-mcp confirmation state v1\n"
)

var (
	errForeignConfirmation = errors.New("this confirmation does not belong to this call; nothing changed. Call the tool again")
	errExpiredConfirmation = errors.New("the confirmation expired; nothing changed. Call the tool again")
	errSpentConfirmation   = errors.New("this confirmation was already used; nothing changed. Call the tool again to confirm anew")
	errConfirmationsFull   = errors.New("ploeg-mcp holds too many open confirmations; nothing changed. Try again in a few minutes")
)

type confirmState struct {
	Tool      string `json:"t"`
	ID        string `json:"i"`
	CommandID string `json:"c"`
	Effect    string `json:"e"`
	Args      string `json:"a"`
	Actor     string `json:"p"`
	Expires   int64  `json:"x"`
}

type confirmations struct {
	key    []byte
	now    func() time.Time
	window time.Duration
	pages  *confirmationPages
	mu     sync.Mutex
	spent  map[string]int64
}

func newConfirmations(key []byte, now func() time.Time) *confirmations {
	return &confirmations{key: key, now: now, window: confirmWindow, spent: map[string]int64{}}
}

func loadConfirmations(getenv func(string) string) (*confirmations, error) {
	key := strings.TrimSpace(getenv(confirmationKeyEnv))
	if key == "" {
		return nil, nil
	}
	if len(key) < minConfirmationKeyBytes {
		return nil, fmt.Errorf("%s must hold at least %d bytes, such as the output of openssl rand -base64 48", confirmationKeyEnv, minConfirmationKeyBytes)
	}
	c := newConfirmations([]byte(key), time.Now)
	if raw := strings.TrimSpace(getenv("PLOEG_MCP_CONFIRMATION_TTL")); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl < time.Minute || ttl > time.Hour {
			return nil, errors.New("PLOEG_MCP_CONFIRMATION_TTL must be a duration between 1m and 1h")
		}
		c.window = ttl
	}
	return c, nil
}

func (c *confirmations) mac(payload string) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(confirmationMACContext))
	h.Write([]byte(payload))
	return h.Sum(nil)
}

func (c *confirmations) seal(s confirmState) string {
	raw, _ := json.Marshal(s)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(c.mac(payload))
}

func (c *confirmations) open(sealed string) (confirmState, error) {
	var s confirmState
	payload, tag, ok := strings.Cut(sealed, ".")
	got, err := base64.RawURLEncoding.DecodeString(tag)
	if !ok || err != nil || !hmac.Equal(got, c.mac(payload)) {
		return s, errForeignConfirmation
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &s) != nil {
		return s, errForeignConfirmation
	}
	if c.now().Unix() > s.Expires {
		return s, errExpiredConfirmation
	}
	return s, nil
}

func (c *confirmations) spend(s confirmState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.spent[s.ID]; seen {
		return errSpentConfirmation
	}
	if len(c.spent) >= maxSpentConfirmations {
		now := c.now().Unix()
		for id, expires := range c.spent {
			if expires < now {
				delete(c.spent, id)
			}
		}
		if len(c.spent) >= maxSpentConfirmations {
			return errConfirmationsFull
		}
	}
	c.spent[s.ID] = s.Expires
	return nil
}
