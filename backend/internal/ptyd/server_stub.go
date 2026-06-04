//go:build !linux

package ptyd

import "log/slog"

func Run(port string) {
	slog.Error("pty.unsupported", "os", "non-linux", "port", port)
}
