package git

import (
	"context"
	"errors"
	"strings"
)

// RemoteURL returns remote's configured fetch URL, or "" when there is no such
// remote or it has none. It is a read: the remote is looked up, never changed.
// The URL is returned exactly as configured (https stays https), because
// publish pins it and a change is drift.
func (s *Service) RemoteURL(ctx context.Context, remote string) (string, error) {
	if remote == "" {
		return "", errors.New("git: remote needs a name")
	}
	out, err := s.run(ctx, "remote", "get-url", remote)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}
