//go:build !linux

package metadata

import (
	"context"
	"errors"
)

// Start is a no-op on non-Linux platforms.
func (s *Server) Start(ctx context.Context) error {
	return errors.New("metadata: server requires Linux")
}
