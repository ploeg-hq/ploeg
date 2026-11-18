// Package team holds a Team's identity (ADR-0198): an immutable generated
// id, a renameable key with aliases, and a free display name.
package team

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Sources of a Team.
const (
	SourceStatic  = "static"
	SourceRuntime = "runtime"
)

// States of a Team (ADR-0198 section 5).
const (
	StateActive   = "active"
	StateShadowed = "shadowed"
	StateInvalid  = "invalid"
)

// Limits of ADR-0198 section 12.
const (
	MaxNameLength = 80
	MaxAliases    = 10
)

// AliasReservation is how long a released key stays reserved (ADR-0198
// section 4).
const AliasReservation = 90 * 24 * time.Hour

// ConfigurationActor attributes a change the boot reconciler makes from
// the configuration file.
const ConfigurationActor = "configuration"

const idPrefix = "team_"

const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

var (
	idPattern         = regexp.MustCompile(`^team_[0-9a-hjkmnp-tv-z]{26}$`)
	runtimeKeyPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
)

// NewID returns a new Team id: "team_" and a lowercase ULID of now and
// 80 bits read from entropy.
func NewID(now time.Time, entropy io.Reader) (string, error) {
	var raw [16]byte
	ms := uint64(now.UnixMilli())
	if ms >= 1<<48 {
		return "", errors.New("team id: time beyond the ULID range")
	}
	binary.BigEndian.PutUint16(raw[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(raw[2:6], uint32(ms))
	if _, err := io.ReadFull(entropy, raw[6:]); err != nil {
		return "", fmt.Errorf("team id: %w", err)
	}
	return idPrefix + encodeULID(raw), nil
}

// MustNewID returns NewID(time.Now(), crypto/rand) and panics only when the
// system's randomness fails.
func MustNewID() string {
	id, err := NewID(time.Now(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return id
}

func encodeULID(raw [16]byte) string {
	var hi, lo uint64
	hi = binary.BigEndian.Uint64(raw[0:8])
	lo = binary.BigEndian.Uint64(raw[8:16])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// IsID reports whether s has the form of a Team id.
func IsID(s string) bool { return idPattern.MatchString(s) }

// ValidRuntimeKey reports whether key is a key a runtime Team may take.
// Static keys are not checked against it.
func ValidRuntimeKey(key string) bool { return runtimeKeyPattern.MatchString(key) }

// ValidateName checks a display name: 1 to 80 characters, no control
// characters.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("a name is 1 to 80 characters and not only spaces")
	}
	if !utf8.ValidString(name) {
		return errors.New("a name is valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return fmt.Errorf("a name is at most %d characters", MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("a name holds no control characters")
		}
	}
	return nil
}

// RoleHarness is one Role of a Team's plan and the Harness it runs on.
type RoleHarness struct {
	Role    string
	Harness string
}

// DefaultName is the display name a Team gets when nobody gave it one: the
// Role and its Harness for a one-Role Team ("builder · openhands"), the key
// for any other.
func DefaultName(key string, roles []RoleHarness) string {
	if len(roles) == 1 && roles[0].Role != "" && roles[0].Harness != "" {
		return roles[0].Role + " · " + roles[0].Harness
	}
	return key
}

// NameSource says where a Team's display name comes from, so a reader can
// show it before anyone tries to change it.
type NameSource struct {
	Source  string `json:"source"`
	Key     string `json:"key,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// Name sources.
const (
	NameFromConfiguration = "configuration"
	NameFromOperatorAPI   = "operator_api"
)

// ConfigurationNameKey is the file key that sets a static Team's name.
func ConfigurationNameKey(key string) string { return "teams." + key + ".name" }

// StaticNameSource is the name source of a static Team.
func StaticNameSource(key string, defaulted bool) NameSource {
	return NameSource{Source: NameFromConfiguration, Key: ConfigurationNameKey(key), Default: defaulted}
}
