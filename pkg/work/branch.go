package work

import "strings"

// Branch derives a Work Item's work branch. A Follow-Up works on its
// SourceBranch. Vikunja items, and items with no recorded provider, keep the
// deployed agent/vik-<id> form byte for byte; every other tracker gets
// agent/<provider>-<id> with characters outside [A-Za-z0-9_-] replaced by '-'.
func Branch(item WorkItem) string {
	if item.SourceBranch != "" {
		return item.SourceBranch
	}
	if item.Provider == "" || item.Provider == "vikunja" {
		return "agent/vik-" + item.ExternalID
	}
	return "agent/" + refSafe(item.Provider) + "-" + refSafe(item.ExternalID)
}

// Reference is the tracker reference an agent puts in commit trailers and the
// pull request body. Vikunja items, and items with no recorded provider, keep
// the deployed VIK-<id> form byte for byte; every other tracker gets
// <provider>-<id>, sanitized the same way as Branch.
func Reference(item WorkItem) string {
	if item.Provider == "" || item.Provider == "vikunja" {
		return "VIK-" + item.ExternalID
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
