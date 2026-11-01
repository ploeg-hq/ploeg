// Package money formats the amounts and counts Ploeg writes for people: on
// a pull request's usage report and on a tracker card. One rendering for
// every surface, nl-NL, hand-rolled rather than locale-dependent so it is
// pure and identical on every host.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// USD formats a US dollar amount: "US$ 0,06", a space after US$, a decimal
// comma and a dot as the thousands separator, rounded half-up to cents.
func USD(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	cents := int64(v*100 + 0.5)
	whole := cents / 100
	frac := cents % 100
	s := groupThousands(strconv.FormatInt(whole, 10)) + "," + fmt.Sprintf("%02d", frac)
	if neg {
		s = "-" + s
	}
	return "US$ " + s
}

// Count formats an integer, such as a token count, with dot thousands
// separators: "15.004".
func Count(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := groupThousands(strconv.FormatInt(n, 10))
	if neg {
		s = "-" + s
	}
	return s
}

// groupThousands inserts a dot every three digits, left to right.
func groupThousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
	}
	for i := first; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
