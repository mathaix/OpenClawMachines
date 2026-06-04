package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// validSchemes are the only allowed URL schemes for custom providers.
var validSchemes = map[string]bool{
	"https": true,
	"http":  true,
}

// blockedHostnames are hostnames that must never be used as upstream hosts.
var blockedHostnames = map[string]bool{
	"localhost":                true,
	"metadata.google.internal": true,
}

// isPrivateOrBlockedIP checks whether an IP is private, loopback, or link-local.
func isPrivateOrBlockedIP(ip net.IP) bool {
	// Loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}
	// Link-local (169.254.0.0/16 — includes GCP metadata at 169.254.169.254)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// RFC1918 private ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
	if ip.IsPrivate() {
		return true
	}
	// Unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}
	return false
}

// validateUpstreamHost checks that the upstream host is not a private, loopback,
// or link-local address, and does not resolve to one. This prevents SSRF attacks.
func validateUpstreamHost(host string) error {
	// Strip port if present
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	lower := strings.ToLower(hostname)

	// Check against blocklist
	if blockedHostnames[lower] {
		return fmt.Errorf("upstream_host %q is not allowed", host)
	}

	// If it's a raw IP, check directly
	if ip := net.ParseIP(hostname); ip != nil {
		if isPrivateOrBlockedIP(ip) {
			return fmt.Errorf("upstream_host %q points to a private or reserved IP address", host)
		}
		return nil
	}

	// It's a hostname — resolve and check all IPs
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return fmt.Errorf("upstream_host %q could not be resolved: %v", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && isPrivateOrBlockedIP(ip) {
			return fmt.Errorf("upstream_host %q resolves to private or reserved IP %s", host, addr)
		}
	}

	return nil
}

// builtinProviders is the hardcoded list of built-in providers.
var builtinProviders = []string{"anthropic", "openai", "google", "openrouter", "telegram", "discord_bot", "whatsapp"}

// validProviderNameRe restricts custom provider names to lowercase alphanumerics, hyphens, and underscores.
var validProviderNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,98}[a-z0-9]$`)

// validAuthMethods are the supported auth methods for custom providers.
var validAuthMethods = map[string]bool{
	"bearer_header":  true,
	"api_key_header": true,
	"query_param":    true,
	"none":           true,
}

// providerListEntry is the response shape for the list providers endpoint.
type providerListEntry struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"` // "builtin" or "custom"
	UpstreamHost string   `json:"upstream_host,omitempty"`
	Scheme       string   `json:"scheme,omitempty"`
	AuthMethod   string   `json:"auth_method,omitempty"`
	AuthHeader   *string  `json:"auth_header,omitempty"`
	PathPrefix   string   `json:"path_prefix,omitempty"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	IsLLM        bool     `json:"is_llm,omitempty"`
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	custom, err := s.store.ListCustomProviders(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entries := make([]providerListEntry, 0, len(builtinProviders)+len(custom))

	// Add built-in providers
	for _, name := range builtinProviders {
		entries = append(entries, providerListEntry{
			Name: name,
			Type: "builtin",
		})
	}

	// Add custom providers
	for _, cp := range custom {
		entries = append(entries, providerListEntry{
			Name:         cp.Name,
			Type:         "custom",
			UpstreamHost: cp.UpstreamHost,
			Scheme:       cp.Scheme,
			AuthMethod:   cp.AuthMethod,
			AuthHeader:   cp.AuthHeader,
			PathPrefix:   cp.PathPrefix,
			AllowedHosts: cp.AllowedHosts,
			IsLLM:        cp.IsLLM,
		})
	}

	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleRegisterProvider(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	var req struct {
		Name         string   `json:"name"`
		UpstreamHost string   `json:"upstream_host"`
		Scheme       string   `json:"scheme"`
		AuthMethod   string   `json:"auth_method"`
		AuthHeader   *string  `json:"auth_header,omitempty"`
		PathPrefix   string   `json:"path_prefix"`
		AllowedHosts []string `json:"allowed_hosts,omitempty"`
		IsLLM        bool     `json:"is_llm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validProviderNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "name must be 2-100 lowercase alphanumeric characters, hyphens, or underscores")
		return
	}

	// Disallow overriding built-in providers
	for _, bp := range builtinProviders {
		if req.Name == bp {
			writeError(w, http.StatusBadRequest, "cannot override built-in provider: "+req.Name)
			return
		}
	}

	if req.UpstreamHost == "" {
		writeError(w, http.StatusBadRequest, "upstream_host is required")
		return
	}

	// SSRF protection: reject private/internal upstream hosts
	if err := validateUpstreamHost(req.UpstreamHost); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.AuthMethod == "" {
		writeError(w, http.StatusBadRequest, "auth_method is required")
		return
	}
	if !validAuthMethods[req.AuthMethod] {
		writeError(w, http.StatusBadRequest, "auth_method must be one of: bearer_header, api_key_header, query_param, none")
		return
	}

	if req.Scheme == "" {
		req.Scheme = "https"
	}
	if !validSchemes[req.Scheme] {
		writeError(w, http.StatusBadRequest, "scheme must be either \"https\" or \"http\"")
		return
	}

	provider := &store.CustomProvider{
		AccountID:    accountID,
		Name:         req.Name,
		UpstreamHost: req.UpstreamHost,
		Scheme:       req.Scheme,
		AuthMethod:   req.AuthMethod,
		AuthHeader:   req.AuthHeader,
		PathPrefix:   req.PathPrefix,
		AllowedHosts: req.AllowedHosts,
		IsLLM:        req.IsLLM,
	}

	if err := s.store.CreateCustomProvider(r.Context(), provider); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, provider)
}

func (s *Server) handleUnregisterProvider(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	if name == "" {
		writeError(w, http.StatusBadRequest, "provider name is required")
		return
	}

	// Disallow deleting built-in providers
	for _, bp := range builtinProviders {
		if name == bp {
			writeError(w, http.StatusBadRequest, "cannot unregister built-in provider: "+name)
			return
		}
	}

	if err := s.store.DeleteCustomProvider(r.Context(), accountID, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
