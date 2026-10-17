package operatorclient

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PersonID is Ploeg's person id for an OpenID Connect identity (ADR-0108):
// "oidc-" followed by the first 32 lowercase hex characters of the SHA-256 of
// "<namespace>|<subject>". namespace is the token's issuer with one trailing
// "/" removed, or the subject namespace a deployment configures in its place
// so that person ids survive a change of issuer. A consumer that names a
// person in X-Ploeg-Actor uses this form, so one person owns the same
// executions through every consumer.
func PersonID(namespace, subject string) string {
	sum := sha256.Sum256([]byte(strings.TrimSuffix(namespace, "/") + "|" + subject))
	return "oidc-" + hex.EncodeToString(sum[:])[:32]
}
