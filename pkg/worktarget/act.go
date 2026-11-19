package worktarget

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ploeg-hq/ploeg/pkg/connection"
	"github.com/ploeg-hq/ploeg/pkg/store"
)

// ErrNoActingConnection is a repository Ploeg may not act on through a
// runtime forge connection: no active runtime target on that forge names
// it, or its connection acts as a person.
var ErrNoActingConnection = errors.New("worktarget: no runtime forge connection acts on the repository")

// ActingToken returns the token Ploeg acts with on repository of forge
// outside a Run, for a pull request's notes, facts and CI: the credential of
// the connection the repository's active runtime target was granted
// through, read afresh so a rotated token is used at once. A connection
// that acts as a person (OAuth) is refused, as its mint is.
func (s *Service) ActingToken(ctx context.Context, forge, repository string) (string, error) {
	acting := ""
	for _, t := range s.Live() {
		if t.State == store.WorkTargetActive && t.Forge == forge && strings.EqualFold(t.Repository, repository) {
			acting = t.Connection
			break
		}
	}
	if acting == "" || s.Connections == nil {
		return "", fmt.Errorf("%w: %s on %s", ErrNoActingConnection, repository, forge)
	}
	cred, err := s.Connections.Credential(ctx, acting)
	if err != nil {
		return "", err
	}
	if cred.Method == connection.MethodOAuth || cred.Token == "" {
		return "", fmt.Errorf("%w: %s on %s through %s", ErrNoActingConnection, repository, forge, acting)
	}
	return cred.Token, nil
}
