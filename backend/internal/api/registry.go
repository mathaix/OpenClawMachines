package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// validRegistryKinds are the allowed values for registry entry type/kind.
var validRegistryKinds = map[string]bool{
	"channel":  true,
	"skill":    true,
	"tool":     true,
	"identity": true,
}

// registryEntryNameRe restricts registry entry names to printable characters, 1-100 chars.
var registryEntryNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9 _-]{0,98}[a-zA-Z0-9]$`)

// ---- Public registry handlers (read-only, no account scope) ----

func (s *Server) handleListRegistryChannels(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListRegistryEntries(r.Context(), "channel")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleListRegistrySkills(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListRegistryEntries(r.Context(), "skill")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleListRegistryTools(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListRegistryEntries(r.Context(), "tool")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleGetRegistryEntry(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "entryId")

	entry, err := s.store.GetRegistryEntry(r.Context(), entryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "registry entry not found")
		return
	}

	writeJSON(w, http.StatusOK, entry)
}

// ---- Admin registry handlers (account-scoped for auth) ----

func (s *Server) handleAdminListRegistryEntries(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")

	if kind != "" && !validRegistryKinds[kind] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid kind: must be one of %s", registryKindsList()))
		return
	}

	entries, err := s.store.ListRegistryEntriesAdmin(r.Context(), kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleAdminGetRegistryEntry(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "entryId")

	entry, err := s.store.GetRegistryEntry(r.Context(), entryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "registry entry not found")
		return
	}

	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleAdminCreateRegistryEntry(w http.ResponseWriter, r *http.Request) {
	if requireOwnerRole(w, r) {
		return
	}

	var req struct {
		Kind                string          `json:"kind"`
		Name                string          `json:"name"`
		Description         *string         `json:"description,omitempty"`
		Spec                json.RawMessage `json:"spec"`
		Modes               json.RawMessage `json:"modes,omitempty"`
		Infrastructure      json.RawMessage `json:"infrastructure,omitempty"`
		RequiredCredentials []string        `json:"required_credentials,omitempty"`
		Tier                string          `json:"tier,omitempty"`
		SortOrder           int             `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate kind
	if req.Kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	if !validRegistryKinds[req.Kind] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid kind: must be one of %s", registryKindsList()))
		return
	}

	// Validate name
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !registryEntryNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "name must be 2-100 alphanumeric characters, spaces, hyphens, or underscores")
		return
	}

	// Validate spec (config_template) is valid JSON
	if len(req.Spec) == 0 {
		writeError(w, http.StatusBadRequest, "spec is required")
		return
	}
	var specCheck interface{}
	if err := json.Unmarshal(req.Spec, &specCheck); err != nil {
		writeError(w, http.StatusBadRequest, "spec must be valid JSON")
		return
	}

	// Validate optional modes is valid JSON array
	if len(req.Modes) > 0 {
		var modesCheck interface{}
		if err := json.Unmarshal(req.Modes, &modesCheck); err != nil {
			writeError(w, http.StatusBadRequest, "modes must be valid JSON")
			return
		}
	}

	// Validate optional infrastructure is valid JSON
	if len(req.Infrastructure) > 0 {
		var infraCheck interface{}
		if err := json.Unmarshal(req.Infrastructure, &infraCheck); err != nil {
			writeError(w, http.StatusBadRequest, "infrastructure must be valid JSON")
			return
		}
	}

	// Generate a slug-style ID from kind and name
	id := generateRegistryEntryID(req.Kind, req.Name)

	// Set defaults
	tier := req.Tier
	if tier == "" {
		tier = "free"
	}
	version := "1"

	entry := &store.RegistryEntry{
		ID:                  id,
		Type:                req.Kind,
		Name:                req.Name,
		Description:         req.Description,
		Version:             version,
		Tier:                tier,
		Modes:               req.Modes,
		ConfigTemplate:      req.Spec,
		Infrastructure:      req.Infrastructure,
		RequiredCredentials: req.RequiredCredentials,
		Status:              "active",
		SortOrder:           req.SortOrder,
	}

	if err := s.store.CreateRegistryEntry(r.Context(), entry); err != nil {
		if store.IsConflict(err) {
			writeError(w, http.StatusConflict, "a registry entry with this kind and name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create registry entry")
		return
	}

	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleAdminUpdateRegistryEntry(w http.ResponseWriter, r *http.Request) {
	if requireOwnerRole(w, r) {
		return
	}

	entryID := chi.URLParam(r, "entryId")

	// Verify entry exists
	existing, err := s.store.GetRegistryEntry(r.Context(), entryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "registry entry not found")
		return
	}

	var req struct {
		Name                *string         `json:"name,omitempty"`
		Description         *string         `json:"description,omitempty"`
		Spec                json.RawMessage `json:"spec,omitempty"`
		Modes               json.RawMessage `json:"modes,omitempty"`
		Infrastructure      json.RawMessage `json:"infrastructure,omitempty"`
		RequiredCredentials []string        `json:"required_credentials,omitempty"`
		Status              *string         `json:"status,omitempty"`
		Tier                *string         `json:"tier,omitempty"`
		SortOrder           *int            `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Apply updates to existing entry
	if req.Name != nil {
		if !registryEntryNameRe.MatchString(*req.Name) {
			writeError(w, http.StatusBadRequest, "name must be 2-100 alphanumeric characters, spaces, hyphens, or underscores")
			return
		}
		existing.Name = *req.Name
	}
	if req.Description != nil {
		existing.Description = req.Description
	}
	if len(req.Spec) > 0 {
		var specCheck interface{}
		if err := json.Unmarshal(req.Spec, &specCheck); err != nil {
			writeError(w, http.StatusBadRequest, "spec must be valid JSON")
			return
		}
		existing.ConfigTemplate = req.Spec
	}
	if len(req.Modes) > 0 {
		var modesCheck interface{}
		if err := json.Unmarshal(req.Modes, &modesCheck); err != nil {
			writeError(w, http.StatusBadRequest, "modes must be valid JSON")
			return
		}
		existing.Modes = req.Modes
	}
	if len(req.Infrastructure) > 0 {
		var infraCheck interface{}
		if err := json.Unmarshal(req.Infrastructure, &infraCheck); err != nil {
			writeError(w, http.StatusBadRequest, "infrastructure must be valid JSON")
			return
		}
		existing.Infrastructure = req.Infrastructure
	}
	if req.RequiredCredentials != nil {
		existing.RequiredCredentials = req.RequiredCredentials
	}
	if req.Status != nil {
		existing.Status = *req.Status
	}
	if req.Tier != nil {
		existing.Tier = *req.Tier
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	// Auto-increment version on each update
	if v, err := strconv.Atoi(existing.Version); err == nil {
		existing.Version = strconv.Itoa(v + 1)
	} else {
		existing.Version = "1"
	}

	if err := s.store.UpdateRegistryEntry(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update registry entry")
		return
	}

	// Re-fetch to get updated timestamps
	updated, err := s.store.GetRegistryEntry(r.Context(), entryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch updated entry")
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleAdminDeleteRegistryEntry(w http.ResponseWriter, r *http.Request) {
	if requireOwnerRole(w, r) {
		return
	}

	entryID := chi.URLParam(r, "entryId")

	if err := s.store.DeleteRegistryEntry(r.Context(), entryID); err != nil {
		writeError(w, http.StatusNotFound, "registry entry not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers ----

// generateRegistryEntryID creates a slug-style ID from kind and name.
// e.g., "channel" + "My Telegram" -> "channel-my-telegram"
func generateRegistryEntryID(kind, name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	// Remove any characters that aren't alphanumeric or hyphens
	var cleaned []byte
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			cleaned = append(cleaned, c)
		}
	}
	slug = string(cleaned)
	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "entry"
	}
	return fmt.Sprintf("%s-%s", kind, slug)
}

// registryKindsList returns a comma-separated list of valid registry kinds.
func registryKindsList() string {
	kinds := make([]string, 0, len(validRegistryKinds))
	for k := range validRegistryKinds {
		kinds = append(kinds, k)
	}
	return strings.Join(kinds, ", ")
}
