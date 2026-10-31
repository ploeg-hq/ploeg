package work

import (
	"strings"
	"sync"
)

// ReferenceNamer names a tracker's reference for one of its items, in the
// form the tracker's own forge integrations recognise.
type ReferenceNamer func(externalID string) string

var (
	namersMu sync.RWMutex
	namers   = map[string]ReferenceNamer{}
)

// RegisterReferenceNamer makes namer the reference of every item of
// provider, for Branch and Reference. Each binary that derives branches or
// references registers the same namers at start, before any Work Item is
// handled. A nil namer removes the registration.
func RegisterReferenceNamer(provider string, namer ReferenceNamer) {
	namersMu.Lock()
	defer namersMu.Unlock()
	if namer == nil {
		delete(namers, provider)
		return
	}
	namers[provider] = namer
}

func namedReference(item WorkItem) (string, bool) {
	namersMu.RLock()
	namer, ok := namers[item.Provider]
	namersMu.RUnlock()
	if !ok {
		return "", false
	}
	ref := refSafe(namer(item.ExternalID))
	return ref, ref != ""
}

// Branch derives a Work Item's work branch. A Follow-Up works on its
// SourceBranch. Vikunja items, and items with no recorded provider, keep the
// deployed agent/vik-<id> form byte for byte; a provider with a registered
// ReferenceNamer gets agent/<its reference>; every other tracker gets
// agent/<provider>-<id> with characters outside [A-Za-z0-9_-] replaced by '-'.
// A Shift keeps the branch it recorded when it opened.
func Branch(item WorkItem) string {
	if item.SourceBranch != "" {
		return item.SourceBranch
	}
	if item.Provider == "" || item.Provider == "vikunja" {
		return "agent/vik-" + item.ExternalID
	}
	if ref, ok := namedReference(item); ok {
		return "agent/" + ref
	}
	return "agent/" + refSafe(item.Provider) + "-" + refSafe(item.ExternalID)
}

// Reference is the tracker reference an agent puts in commit trailers and the
// pull request body. Vikunja items, and items with no recorded provider, keep
// the deployed VIK-<id> form byte for byte; a provider with a registered
// ReferenceNamer gets its reference; every other tracker gets
// <provider>-<id>, sanitized the same way as Branch.
func Reference(item WorkItem) string {
	if item.Provider == "" || item.Provider == "vikunja" {
		return "VIK-" + item.ExternalID
	}
	if ref, ok := namedReference(item); ok {
		return ref
	}
	return refSafe(item.Provider) + "-" + refSafe(item.ExternalID)
}

func refSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '-'
	}, s)
}
