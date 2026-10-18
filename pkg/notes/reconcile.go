package notes

import "time"

// Decision is what a new note does to the current notes on its subject.
type Decision struct {
	// Supersedes is the key of the current note the new one replaces, or "".
	Supersedes string
	// Contradicts are the keys of current notes the new one disagrees with;
	// each opens a flag a person decides. The new note is still written.
	Contradicts []string
}

// Reconcile decides, from the notes the graph holds on incoming's subject,
// whether incoming replaces one of them or disagrees with them. briefedAt
// is when the writing Run read its notes; zero when its writer read none.
//
// A current note in the same scope is replaced when the new one says the
// same, when its author updates their own note, when a trusted note corrects
// an untrusted one, when ploegd records a newer observation of its own, when
// a Run that was briefed with it writes a Work Item note on the same
// subject, or when the writer names it in Supersedes and may replace it. Any
// other current note on the subject that says something else is a
// contradiction: it is never kept silently beside the new one.
func Reconcile(incoming Note, existing []Note, briefedAt time.Time) Decision {
	superseded := map[string]bool{}
	for _, n := range existing {
		if n.Supersedes != "" {
			superseded[n.Supersedes] = true
		}
	}
	var current []Note
	for _, n := range existing {
		if n.Key == incoming.Key || n.Type == TypeRetraction || superseded[n.Key] ||
			n.Expired(incoming.WrittenAt) || !sameScope(n, incoming) {
			continue
		}
		current = append(current, n)
	}
	var d Decision
	for _, n := range current {
		if d.Supersedes == "" && mayReplace(incoming, n, briefedAt) {
			d.Supersedes = n.Key
			continue
		}
		if !sameText(n, incoming) {
			d.Contradicts = append(d.Contradicts, n.Key)
		}
	}
	return d
}

func mayReplace(incoming, n Note, briefedAt time.Time) bool {
	if n.Trust == Trusted && incoming.Trust == Untrusted {
		return false
	}
	switch {
	case incoming.Supersedes == n.Key:
		return true
	case sameText(incoming, n):
		return true
	case incoming.AuthorKind == n.AuthorKind && incoming.Author == n.Author:
		return true
	case incoming.Trust == Trusted && n.Trust == Untrusted:
		return true
	case incoming.AuthorKind == AuthorPloeg && n.AuthorKind == AuthorPloeg:
		return true
	case incoming.Level == LevelWorkItem && incoming.AuthorKind == AuthorRun && n.AuthorKind != AuthorPerson &&
		!briefedAt.IsZero() && n.WrittenAt.Before(briefedAt):
		return true
	}
	return false
}
