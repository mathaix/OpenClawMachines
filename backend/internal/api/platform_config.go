package api

import (
	"net/http"
	"strings"
)

type platformConfigResponse struct {
	DataPlaneDomain               string `json:"data_plane_domain"`
	AuthMode                      string `json:"auth_mode"`
	FrontendURL                   string `json:"frontend_url"`
	CfAccessAuthDomain            string `json:"cf_access_auth_domain"`
	RuntimeVersionResolverEnabled bool   `json:"runtime_version_resolver_enabled"`
}

func (s *Server) handlePlatformConfig(w http.ResponseWriter, r *http.Request) {
	// CF_ACCESS_TEAM_DOMAIN stores the team name (e.g. "openclawmachines"),
	// but clients need the full auth domain ("openclawmachines.cloudflareaccess.com").
	cfDomain := s.cfAccessAuthDomain
	if cfDomain != "" && !strings.Contains(cfDomain, ".") {
		cfDomain += ".cloudflareaccess.com"
	}

	writeJSON(w, http.StatusOK, platformConfigResponse{
		DataPlaneDomain:               s.dataPlaneDomain,
		AuthMode:                      s.authMode,
		FrontendURL:                   s.frontendURL(),
		CfAccessAuthDomain:            cfDomain,
		RuntimeVersionResolverEnabled: s.machines.RuntimeVersionResolverEnabled(),
	})
}
