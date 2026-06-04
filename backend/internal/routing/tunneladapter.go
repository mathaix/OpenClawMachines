package routing

import (
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
)

// Compile-time assertion: *tunnel.Manager satisfies TunnelManager directly.
// No adapter is needed — method signatures match exactly.
var _ TunnelManager = (*tunnel.Manager)(nil)
