//go:build unix && !linux

package launch

import "context"

func (s *server) reapOrphans(context.Context) {}
