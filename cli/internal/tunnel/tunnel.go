package tunnel

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/cloudflare/cloudflared/carrier"
	"github.com/rs/zerolog"
)

// Connect opens a WebSocket tunnel to an SSH endpoint via Cloudflare and pipes
// stdin/stdout through it. It is designed to be used as an SSH ProxyCommand.
//
// CF Access is not used on SSH routes — the tunnel is a pure transport layer.
// Authentication is handled by sshd cert validation inside the VM.
func Connect(hostname string) error {
	log := zerolog.New(os.Stderr).Level(zerolog.WarnLevel)

	headers := make(http.Header)
	headers.Set("User-Agent", "ocm-cli")

	options := &carrier.StartOptions{
		OriginURL: "https://" + hostname,
		Headers:   headers,
		Host:      hostname,
	}

	wsConn := carrier.NewWSConnection(&log)
	err := carrier.StartClient(wsConn, &carrier.StdinoutStream{}, options)
	if err != nil {
		if strings.Contains(err.Error(), "bad handshake") {
			return fmt.Errorf("tunnel connection failed for %s — the machine's tunnel may not be running or CF Access may still be enabled on this route: %w", hostname, err)
		}
		return fmt.Errorf("tunnel connection failed for %s: %w", hostname, err)
	}
	return nil
}
