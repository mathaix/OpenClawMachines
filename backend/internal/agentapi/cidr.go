package agentapi

import (
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// cidrAllowlist returns middleware that restricts access to requests from the
// given comma-separated CIDR ranges. If allowedCIDRs is empty, a no-op
// middleware is returned (allow all, for backward compatibility).
func cidrAllowlist(allowedCIDRs string) func(http.Handler) http.Handler {
	trimmed := strings.TrimSpace(allowedCIDRs)
	if trimmed == "" {
		// No restriction configured — allow all traffic but warn loudly.
		return warnAndAllow("empty")
	}

	var nets []*net.IPNet
	for _, cidr := range strings.Split(trimmed, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Printf("cidr_allowlist: ignoring invalid CIDR %q: %v", cidr, err)
			continue
		}
		nets = append(nets, ipNet)
	}

	if len(nets) == 0 {
		log.Printf("cidr_allowlist: no valid CIDRs parsed from %q — allowing all", allowedCIDRs)
		return warnAndAllow("no_valid_cidrs")
	}

	log.Printf("cidr_allowlist: restricting control API to %d CIDR(s)", len(nets))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ipStr := extractIP(r.RemoteAddr)
			ip := net.ParseIP(ipStr)
			if ip == nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			for _, n := range nets {
				if n.Contains(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}

			http.Error(w, "Forbidden", http.StatusForbidden)
		})
	}
}

// warnAndAllow returns middleware that logs a warning on every request because
// the CIDR allowlist is not effectively configured, then allows the request
// through. This is the warn-only phase; a future change will make this
// fail-closed.
func warnAndAllow(reason string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slog.Warn("control_api.cidr_allowlist.not_configured",
				"reason", reason,
				"remote_addr", r.RemoteAddr,
				"path", r.URL.Path,
			)
			next.ServeHTTP(w, r)
		})
	}
}

// extractIP strips the port from a host:port address. If there is no port,
// the input is returned as-is.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// No port — return as-is (may already be a bare IP).
		return remoteAddr
	}
	return host
}
