# Agent CRUD Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable control-plane management of OpenClaw agents (AI personas) within machines, with dashboard UI, config assembly, soul file delivery, and self-config skill support.

**Architecture:** Agent definitions are stored in PostgreSQL (`machine_agents` table), rendered into gateway config via config assembly, and pushed to VMs via the metadata server. Soul files (`SOUL.md`) are delivered separately — hot-pushed via a new `/write-file` PTY endpoint, or cold-delivered via the VM-create payload and metadata server `/v1/souls` endpoint.

**Tech Stack:** Go 1.25 (backend), PostgreSQL/pgx (store), Chi router (API), React 18 + TypeScript (frontend), Vitest (frontend tests)

**Spec:** `docs/designs/agent-crud.md`

---

## File Structure

### New Files
| File | Responsibility |
|------|---------------|
| `backend/migrations/036_machine_agents.sql` | Database migration |
| `backend/internal/api/machine_agents.go` | JWT-auth CRUD handlers for dashboard |
| `backend/internal/api/machine_agents_test.go` | Handler tests |
| `backend/internal/api/agent_auth.go` | Agent-token-auth handlers for self-config |
| `backend/internal/api/agent_auth_test.go` | Agent-auth handler tests |
| `frontend/src/components/MachineAgents.tsx` | Agent list + create/edit/delete UI |
| `frontend/src/components/MachineAgents.test.tsx` | Frontend tests |

### Modified Files
| File | Change |
|------|--------|
| `backend/internal/store/store.go` | Add `MachineAgent` struct + `MachineAgentRepo` interface |
| `backend/internal/store/postgres.go` | Implement CRUD methods |
| `backend/internal/configassembly/assembler.go` | Add `AgentDefinition`, `Agents` field to `AssemblyParams`, step 5d |
| `backend/internal/configassembly/assembler_test.go` | Agent assembly tests |
| `backend/internal/api/server.go` | Register agent routes (JWT + agent-token auth) |
| `backend/internal/api/machine_config.go` | Extend `assembleConfigForMachine` + `handlePushMachineConfig` |
| `backend/cmd/agent/ptyserver.go` | Add `/write-file` endpoint |
| `backend/internal/agentapi/proxy.go` | Add `/write-file` proxy handler |
| `backend/internal/agentapi/server.go` | Register `/write-file` proxy route |
| `backend/internal/agentclient/client.go` | Add `Souls` to `VMRequest`, add `WriteFile` method |
| `backend/internal/metadata/metadata.go` | Add `Souls`, `BackendURL`, `AgentToken` to structs |
| `backend/internal/metadata/server_linux.go` | Add `/v1/souls` + `/v1/admin/*` endpoints |
| `backend/internal/metadata/server_linux_test.go` | Metadata endpoint tests |
| `backend/internal/machines/runtime.go` | Fetch agents + build souls in Start() |
| `backend/internal/orchestrator/types.go` | Add `Souls` to `VMConfig` |
| `backend/internal/orchestrator/firecracker_linux.go` | Pass souls to RegisterMachine |
| `scripts/init-openclaw.sh` | Fetch + write soul files before gateway start |
| `frontend/src/lib/api.ts` | Add agent API client functions |

---

## Chunk 1: Database + Store Layer

### Task 1: Database Migration

**Files:**
- Create: `backend/migrations/036_machine_agents.sql`

- [ ] **Step 1: Write the migration file**

```sql
-- 036: Add machine_agents table for control-plane-managed OpenClaw personas

CREATE TABLE IF NOT EXISTS machine_agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    agent_id        TEXT NOT NULL,
    name            TEXT NOT NULL,
    model           TEXT,
    identity_emoji  TEXT,
    identity_avatar TEXT,
    soul            TEXT,
    is_default      BOOLEAN NOT NULL DEFAULT false,
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_machine_agents_machine_id ON machine_agents(machine_id);
```

- [ ] **Step 2: Verify migration file exists and is valid SQL**

Run: `cat backend/migrations/036_machine_agents.sql`
Expected: The SQL above, no syntax errors.

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/036_machine_agents.sql
git commit -m "feat: add machine_agents migration (036)"
```

---

### Task 2: Store Types and Interface

**Files:**
- Modify: `backend/internal/store/store.go`

- [ ] **Step 1: Add the MachineAgent struct**

Add after the `MachineCapability` struct (around line 252):

```go
// MachineAgent represents an OpenClaw persona (AI agent) within a machine.
type MachineAgent struct {
	ID             string    `json:"id"`
	MachineID      string    `json:"machine_id"`
	AgentID        string    `json:"agent_id"`
	Name           string    `json:"name"`
	Model          *string   `json:"model,omitempty"`
	IdentityEmoji  *string   `json:"identity_emoji,omitempty"`
	IdentityAvatar *string   `json:"identity_avatar,omitempty"`
	Soul           *string   `json:"soul,omitempty"`
	IsDefault      bool      `json:"is_default"`
	SortOrder      int       `json:"sort_order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
```

- [ ] **Step 2: Add the MachineAgentRepo interface**

Add after the existing `BackupRepo` interface (around line 645):

```go
// MachineAgentRepo handles CRUD for OpenClaw personas within machines.
type MachineAgentRepo interface {
	CreateMachineAgent(ctx context.Context, agent *MachineAgent) error
	ListMachineAgents(ctx context.Context, machineID string) ([]MachineAgent, error)
	GetMachineAgent(ctx context.Context, machineID, agentID string) (*MachineAgent, error)
	UpdateMachineAgent(ctx context.Context, agent *MachineAgent) error
	DeleteMachineAgent(ctx context.Context, machineID, agentID string) error
}
```

- [ ] **Step 3: Add MachineAgentRepo to the Store aggregate interface**

Find the `Store` interface (around line 651-669) and add `MachineAgentRepo`:

```go
type Store interface {
	// ... existing repos ...
	MachineAgentRepo
}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/store/...`
Expected: Compile error about PostgresStore not implementing MachineAgentRepo (expected — we implement next).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/store.go
git commit -m "feat: add MachineAgent struct and MachineAgentRepo interface"
```

---

### Task 3: Store Implementation

**Files:**
- Modify: `backend/internal/store/postgres.go`

- [ ] **Step 1: Implement CreateMachineAgent**

Add at end of file (follow BackupRecord pattern at line 2677):

```go
// --- MachineAgentRepo ---

func (s *PostgresStore) CreateMachineAgent(ctx context.Context, agent *MachineAgent) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO machine_agents (machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul, is_default, sort_order)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at, updated_at`,
		agent.MachineID, agent.AgentID, agent.Name, agent.Model,
		agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
		agent.IsDefault, agent.SortOrder,
	).Scan(&agent.ID, &agent.CreatedAt, &agent.UpdatedAt)
}
```

- [ ] **Step 2: Implement ListMachineAgents**

```go
func (s *PostgresStore) ListMachineAgents(ctx context.Context, machineID string) ([]MachineAgent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul,
		        is_default, sort_order, created_at, updated_at
		 FROM machine_agents WHERE machine_id = $1 ORDER BY sort_order, created_at`,
		machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []MachineAgent
	for rows.Next() {
		var a MachineAgent
		if err := rows.Scan(&a.ID, &a.MachineID, &a.AgentID, &a.Name, &a.Model,
			&a.IdentityEmoji, &a.IdentityAvatar, &a.Soul,
			&a.IsDefault, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	if agents == nil {
		agents = []MachineAgent{}
	}
	return agents, rows.Err()
}
```

- [ ] **Step 3: Implement GetMachineAgent**

```go
func (s *PostgresStore) GetMachineAgent(ctx context.Context, machineID, agentID string) (*MachineAgent, error) {
	var a MachineAgent
	err := s.pool.QueryRow(ctx,
		`SELECT id, machine_id, agent_id, name, model, identity_emoji, identity_avatar, soul,
		        is_default, sort_order, created_at, updated_at
		 FROM machine_agents WHERE machine_id = $1 AND agent_id = $2`,
		machineID, agentID,
	).Scan(&a.ID, &a.MachineID, &a.AgentID, &a.Name, &a.Model,
		&a.IdentityEmoji, &a.IdentityAvatar, &a.Soul,
		&a.IsDefault, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
```

- [ ] **Step 4: Implement UpdateMachineAgent**

```go
func (s *PostgresStore) UpdateMachineAgent(ctx context.Context, agent *MachineAgent) error {
	result, err := s.pool.Exec(ctx,
		`UPDATE machine_agents
		 SET name = $3, model = $4, identity_emoji = $5, identity_avatar = $6,
		     soul = $7, is_default = $8, sort_order = $9, updated_at = now()
		 WHERE machine_id = $1 AND agent_id = $2`,
		agent.MachineID, agent.AgentID, agent.Name, agent.Model,
		agent.IdentityEmoji, agent.IdentityAvatar, agent.Soul,
		agent.IsDefault, agent.SortOrder)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("agent not found")
	}
	return nil
}
```

- [ ] **Step 5: Implement DeleteMachineAgent**

```go
func (s *PostgresStore) DeleteMachineAgent(ctx context.Context, machineID, agentID string) error {
	result, err := s.pool.Exec(ctx,
		`DELETE FROM machine_agents WHERE machine_id = $1 AND agent_id = $2`,
		machineID, agentID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("agent not found")
	}
	return nil
}
```

- [ ] **Step 6: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Clean build, no errors.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/store/postgres.go
git commit -m "feat: implement MachineAgent CRUD in PostgresStore"
```

---

## Chunk 2: Config Assembly

### Task 4: Config Assembly — AgentDefinition + Step 5d

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write failing tests for agent assembly**

Add to `assembler_test.go` after `TestAgentsDefaults` (around line 907):

```go
func TestAssembleConfig_WithAgents(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Agents: []AgentDefinition{
			{AgentID: "support", Name: "Support Bot", IsDefault: true, IdentityEmoji: "\U0001F916"},
			{AgentID: "creative", Name: "Creative Writer", Model: "anthropic/claude-opus-4-6", IdentityEmoji: "\u270D\uFE0F"},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, ok := getNestedMap(m, "agents")
	if !ok {
		t.Fatal("missing agents key")
	}
	list, ok := agents["list"].([]interface{})
	if !ok || len(list) != 2 {
		t.Fatalf("agents.list: got %v, want 2-element array", agents["list"])
	}
	// First agent is default
	a0 := list[0].(map[string]interface{})
	if a0["id"] != "support" {
		t.Errorf("agents.list[0].id = %v, want support", a0["id"])
	}
	if a0["default"] != true {
		t.Errorf("agents.list[0].default = %v, want true", a0["default"])
	}
	if a0["workspace"] != "/home/openclaw/.openclaw/workspace" {
		t.Errorf("default agent workspace = %v, want /home/openclaw/.openclaw/workspace", a0["workspace"])
	}
	// Second agent has model override
	a1 := list[1].(map[string]interface{})
	if a1["id"] != "creative" {
		t.Errorf("agents.list[1].id = %v, want creative", a1["id"])
	}
	model1 := a1["model"].(map[string]interface{})
	if model1["primary"] != "anthropic/claude-opus-4-6" {
		t.Errorf("agent model.primary = %v, want anthropic/claude-opus-4-6", model1["primary"])
	}
	if a1["workspace"] != "/home/openclaw/.openclaw/workspace-creative" {
		t.Errorf("non-default workspace = %v, want workspace-creative", a1["workspace"])
	}
	// Override model should be in agents.defaults.models
	defaults := agents["defaults"].(map[string]interface{})
	models := defaults["models"].(map[string]interface{})
	if _, ok := models["anthropic/claude-opus-4-6"]; !ok {
		t.Error("override model missing from agents.defaults.models")
	}
}

func TestAssembleConfig_NoAgents_BackwardCompatible(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, ok := getNestedMap(m, "agents")
	if !ok {
		t.Fatal("missing agents key")
	}
	if _, ok := agents["list"]; ok {
		t.Error("agents.list should not exist when no agents provided")
	}
}

func TestAssembleConfig_AgentIdentity(t *testing.T) {
	data, err := AssembleConfig(AssemblyParams{
		MachineID:    "m-1",
		DefaultModel: "anthropic/claude-sonnet-4-6",
		Agents: []AgentDefinition{
			{AgentID: "bot", Name: "Bot", IsDefault: true, IdentityEmoji: "\U0001F916", IdentityAvatar: "robot"},
		},
	})
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}
	m := mustUnmarshalMap(t, data)
	agents, _ := getNestedMap(m, "agents")
	list := agents["list"].([]interface{})
	a0 := list[0].(map[string]interface{})
	identity := a0["identity"].(map[string]interface{})
	if identity["emoji"] != "\U0001F916" {
		t.Errorf("identity.emoji = %v", identity["emoji"])
	}
	if identity["avatar"] != "robot" {
		t.Errorf("identity.avatar = %v", identity["avatar"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestAssembleConfig_WithAgents|TestAssembleConfig_NoAgents_BackwardCompatible|TestAssembleConfig_AgentIdentity" -v`
Expected: FAIL — `AgentDefinition` undefined, `Agents` field not in `AssemblyParams`.

- [ ] **Step 3: Add AgentDefinition struct and Agents field to AssemblyParams**

In `assembler.go`, add after the `Identity` struct (around line 210):

```go
// AgentDefinition represents an OpenClaw persona for config assembly.
type AgentDefinition struct {
	AgentID        string
	Name           string
	Model          string // empty = inherit default
	IdentityEmoji  string
	IdentityAvatar string
	IsDefault      bool
	SortOrder      int
}
```

Add `Agents` field to `AssemblyParams` (around line 224):

```go
type AssemblyParams struct {
	MachineID       string
	Capabilities    []CapabilityWithTemplate
	Identity        *Identity
	Credentials     map[string]string
	ProxyBaseURL    string
	DefaultModel    string
	VMHostname      string
	AccountHostname string
	BrowserVMIP     string
	BridgeIP        string
	Agents          []AgentDefinition // per-machine OpenClaw personas
}
```

- [ ] **Step 4: Implement step 5d — render agents.list**

In `assembler.go`, after step 5c (after line 349, the closing `}` of the `if params.DefaultModel != ""` block):

```go
	// 5d. Render agents.list from AgentDefinition slice
	if len(params.Agents) > 0 && params.DefaultModel != "" {
		agents := getOrCreateMap(result, "agents")
		defaults := getOrCreateMap(agents, "defaults")
		modelsMap, _ := defaults["models"].(map[string]interface{})
		if modelsMap == nil {
			modelsMap = map[string]interface{}{}
		}

		var list []interface{}
		for _, a := range params.Agents {
			entry := map[string]interface{}{
				"id":   a.AgentID,
				"name": a.Name,
			}
			if a.IsDefault {
				entry["default"] = true
				entry["workspace"] = "/home/openclaw/.openclaw/workspace"
			} else {
				entry["workspace"] = "/home/openclaw/.openclaw/workspace-" + a.AgentID
			}
			// Model: use override or inherit default
			model := params.DefaultModel
			if a.Model != "" {
				model = a.Model
				modelsMap[a.Model] = map[string]interface{}{}
			}
			entry["model"] = map[string]interface{}{"primary": model}
			// Identity
			identity := map[string]interface{}{}
			if a.IdentityEmoji != "" {
				identity["emoji"] = a.IdentityEmoji
			}
			if a.IdentityAvatar != "" {
				identity["avatar"] = a.IdentityAvatar
			}
			if len(identity) > 0 {
				entry["identity"] = identity
			}
			list = append(list, entry)
		}
		agents["list"] = list
		defaults["models"] = modelsMap
		agents["defaults"] = defaults
		result["agents"] = agents
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -run "TestAssembleConfig_WithAgents|TestAssembleConfig_NoAgents_BackwardCompatible|TestAssembleConfig_AgentIdentity" -v`
Expected: PASS

- [ ] **Step 6: Run full config assembly test suite**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/ -v`
Expected: All tests PASS (existing tests unaffected).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go
git commit -m "feat: add agents.list rendering to config assembly (step 5d)"
```

---

## Chunk 3: Backend API — CRUD Handlers

### Task 5: JWT-Auth Agent CRUD Handlers

**Files:**
- Create: `backend/internal/api/machine_agents.go`
- Create: `backend/internal/api/machine_agents_test.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create machine_agents.go with CRUD handlers**

Follow the pattern from `machine_credentials.go` (lines 10-31): extract machine ID, validate ownership, call store, return JSON.

```go
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

var validAgentID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Server) handleListMachineAgents(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	agents, err := s.store.ListMachineAgents(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleCreateMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	var req struct {
		AgentID        string  `json:"agent_id"`
		Name           string  `json:"name"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      bool    `json:"is_default"`
		SortOrder      int     `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "agent_id and name are required")
		return
	}
	if !validAgentID.MatchString(req.AgentID) {
		writeError(w, http.StatusBadRequest, "agent_id must be lowercase alphanumeric with hyphens, starting with a letter")
		return
	}
	if err := s.validateAgentModel(r.Context(), machineID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agent := &store.MachineAgent{
		MachineID:      machineID,
		AgentID:        req.AgentID,
		Name:           req.Name,
		Model:          req.Model,
		IdentityEmoji:  req.IdentityEmoji,
		IdentityAvatar: req.IdentityAvatar,
		Soul:           req.Soul,
		IsDefault:      req.IsDefault,
		SortOrder:      req.SortOrder,
	}

	if err := s.store.CreateMachineAgent(r.Context(), agent); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "agent_id already exists on this machine")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.created", "machine_id", machineID, "agent_id", req.AgentID)
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleGetMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	agent, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleUpdateMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	existing, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	var req struct {
		Name           *string `json:"name,omitempty"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      *bool   `json:"is_default,omitempty"`
		SortOrder      *int    `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.validateAgentModel(r.Context(), machineID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Partial update: only overwrite fields that are present in the request
	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Model != nil {
		existing.Model = req.Model
	}
	if req.IdentityEmoji != nil {
		existing.IdentityEmoji = req.IdentityEmoji
	}
	if req.IdentityAvatar != nil {
		existing.IdentityAvatar = req.IdentityAvatar
	}
	if req.Soul != nil {
		existing.Soul = req.Soul
	}
	if req.IsDefault != nil {
		existing.IsDefault = *req.IsDefault
	}
	if req.SortOrder != nil {
		existing.SortOrder = *req.SortOrder
	}

	if err := s.store.UpdateMachineAgent(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.updated", "machine_id", machineID, "agent_id", agentID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteMachineAgent(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	// Check if trying to delete default agent
	existing, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if existing.IsDefault {
		writeError(w, http.StatusBadRequest, "cannot delete the default agent; assign a new default first")
		return
	}

	if err := s.store.DeleteMachineAgent(r.Context(), machineID, agentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.deleted", "machine_id", machineID, "agent_id", agentID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// isUniqueViolation checks if a pgx error is a unique constraint violation.
func isUniqueViolation(err error) bool {
	return err != nil && (
		// pgx wraps pq errors; check the error string for the PG unique_violation code
		fmt.Sprintf("%v", err) == "ERROR: duplicate key value violates unique constraint" ||
		// More robust: check if pgconn.PgError with Code 23505
		containsCode(err, "23505"))
}

func containsCode(err error, code string) bool {
	if err == nil {
		return false
	}
	// Use type assertion for pgconn.PgError
	type pgErr interface{ SQLState() string }
	if pe, ok := err.(pgErr); ok {
		return pe.SQLState() == code
	}
	return false
}

// validateAgentModel validates a per-agent model override against both the
// platform allowlist and the machine's credential entries.
func (s *Server) validateAgentModel(ctx context.Context, machineID string, model *string) error {
	if model == nil || *model == "" {
		return nil
	}
	if !allowedModels[*model] {
		return fmt.Errorf("model %q is not in the allowed models list", *model)
	}
	// Verify the machine has credentials for this model's provider
	creds, err := s.store.ListMachineCredentials(ctx, machineID)
	if err != nil {
		return fmt.Errorf("failed to check credentials: %w", err)
	}
	provider := strings.Split(*model, "/")[0] // e.g., "anthropic" from "anthropic/claude-sonnet-4-6"
	for _, c := range creds {
		if c.Provider == provider {
			return nil
		}
	}
	return fmt.Errorf("machine has no credentials for provider %q required by model %q", provider, *model)
}
```

- [ ] **Step 2: Register routes in server.go**

In `server.go`, inside the `r.Route("/{id}", ...)` block (around line 357, after capability routes):

```go
				// Agent (persona) routes
				r.Get("/agents", srv.handleListMachineAgents)
				r.Get("/agents/{agentId}", srv.handleGetMachineAgent)
```

Inside the `r.Group(func(r chi.Router) { r.Use(requireRole("owner", "admin"))` block (around line 378):

```go
					r.Post("/agents", srv.handleCreateMachineAgent)
					r.Put("/agents/{agentId}", srv.handleUpdateMachineAgent)
					r.Delete("/agents/{agentId}", srv.handleDeleteMachineAgent)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/api/...`
Expected: Clean build.

- [ ] **Step 4: Run existing tests to verify no regressions**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/machine_agents.go backend/internal/api/server.go
git commit -m "feat: add agent CRUD API handlers with route registration"
```

---

## Chunk 4: PTY /write-file + Agent Proxy

### Task 6: PTY Server /write-file Endpoint

**Files:**
- Modify: `backend/cmd/agent/ptyserver.go`

- [ ] **Step 1: Add /write-file handler to PTY server**

In `ptyserver.go`, after the `/exec` handler (around line 415, before the WebSocket handler):

```go
	// /write-file endpoint — writes allowed files to the workspace
	http.HandleFunc("/write-file", func(w http.ResponseWriter, r *http.Request) {
		writeErr := func(status int, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		}

		if r.Method != http.MethodPost {
			writeErr(http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Path == "" || req.Content == "" {
			writeErr(http.StatusBadRequest, "path and content are required")
			return
		}

		const allowedBase = "/home/openclaw/.openclaw/"
		allowedFiles := map[string]bool{"SOUL.md": true}
		const maxSize = 1024 * 1024 // 1MB

		if len(req.Content) > maxSize {
			writeErr(http.StatusBadRequest, "content too large (max 1MB)")
			return
		}

		// Normalize path
		cleanPath := filepath.Clean(req.Path)
		absPath, err := filepath.Abs(cleanPath)
		if err != nil {
			writeErr(http.StatusBadRequest, "invalid path")
			return
		}

		// Verify filename in allowlist
		if !allowedFiles[filepath.Base(absPath)] {
			writeErr(http.StatusForbidden, "filename not allowed")
			return
		}

		// Resolve symlinks on parent dir
		parentDir := filepath.Dir(absPath)
		realParent, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			// Parent doesn't exist — walk up to verify within allowed base
			ancestor := parentDir
			for ancestor != "/" {
				if rp, e := filepath.EvalSymlinks(ancestor); e == nil {
					if !strings.HasPrefix(rp+"/", allowedBase) {
						writeErr(http.StatusForbidden, "path outside allowed directory")
						return
					}
					break
				}
				ancestor = filepath.Dir(ancestor)
			}
			realParent = parentDir
		}
		realPath := filepath.Join(realParent, filepath.Base(absPath))

		if !strings.HasPrefix(realPath, allowedBase) {
			writeErr(http.StatusForbidden, "path outside allowed directory")
			return
		}

		// Create parent dirs and write file
		if err := os.MkdirAll(filepath.Dir(realPath), 0755); err != nil {
			writeErr(http.StatusInternalServerError, "failed to create directory")
			return
		}
		if err := os.WriteFile(realPath, []byte(req.Content), 0644); err != nil {
			writeErr(http.StatusInternalServerError, "failed to write file")
			return
		}

		// chown to openclaw user (uid 1000)
		_ = os.Chown(realPath, 1000, 1000)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
```

Add `"path/filepath"` and `"strings"` to imports if not present.

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/cmd/agent/...`
Expected: Clean build.

- [ ] **Step 3: Commit**

```bash
git add backend/cmd/agent/ptyserver.go
git commit -m "feat: add /write-file PTY endpoint for soul file delivery"
```

---

### Task 7: Agent Proxy /write-file Route

**Files:**
- Modify: `backend/internal/agentapi/proxy.go`
- Modify: `backend/internal/agentapi/server.go`

- [ ] **Step 1: Add handleWriteFileProxy to proxy.go**

Follow the `handleExecProxy` pattern (line 656):

```go
// handleWriteFileProxy proxies POST /write-file to the PTY server inside the VM.
// Follows the same pattern as handleExecProxy (proxy.go:656) and the
// backend's handleMachineExec (machine_exec.go:14) — uses X-Proxy-Token for auth.
func (s *Server) handleWriteFileProxy(w http.ResponseWriter, r *http.Request) {
	mi, err := s.getMachineInfo(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !validateProxyToken(w, r, mi.ProxyToken) {
		return
	}

	targetURL := fmt.Sprintf("http://%s:7681/write-file", mi.VMIP)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(targetURL, "application/json", r.Body)
	if err != nil {
		http.Error(w, "failed to reach VM", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

- [ ] **Step 2: Register route in server.go**

In `agentapi/server.go`, inside `ProxyRouter()` (around line 140, after the `/exec` route):

```go
	r.Post("/proxy/{machineID}/write-file", s.handleWriteFileProxy)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/agentapi/...`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/agentapi/proxy.go backend/internal/agentapi/server.go
git commit -m "feat: add /write-file proxy route for soul file delivery"
```

---

## Chunk 5: Config Push Integration

### Task 8: Extend Config Push with Agents + Soul Write

**Files:**
- Modify: `backend/internal/api/machine_config.go`
- Modify: `backend/internal/configassembly/assembler.go` (AgentDefinition already added in Task 4)

- [ ] **Step 1: Extend assembleConfigForMachine to load agents**

In `machine_config.go`, inside `assembleConfigForMachine()`, after loading capabilities and before the `AssembleConfig` call (around line 349):

```go
	// Load agents (personas) for this machine
	machineAgents, err := s.store.ListMachineAgents(ctx, machineID)
	if err != nil {
		slog.Warn("machine.config.agents_load_failed", "machine_id", machineID, "error", err)
		// Non-fatal — continue without agents
	}

	var agentDefs []configassembly.AgentDefinition
	if len(machineAgents) > 0 && defaultModel == "" {
		warnings = append(warnings, "agents defined but no default model — agents.list skipped")
	} else {
		for _, a := range machineAgents {
			ad := configassembly.AgentDefinition{
				AgentID:   a.AgentID,
				Name:      a.Name,
				IsDefault: a.IsDefault,
				SortOrder: a.SortOrder,
			}
			if a.Model != nil {
				ad.Model = *a.Model
			}
			if a.IdentityEmoji != nil {
				ad.IdentityEmoji = *a.IdentityEmoji
			}
			if a.IdentityAvatar != nil {
				ad.IdentityAvatar = *a.IdentityAvatar
			}
			agentDefs = append(agentDefs, ad)
		}
	}
```

Then add `Agents: agentDefs` to the `AssemblyParams` struct literal (around line 362):

```go
	params := configassembly.AssemblyParams{
		// ... existing fields ...
		Agents:          agentDefs,
	}
```

- [ ] **Step 2: Add WriteFile method to agentclient**

In `backend/internal/agentclient/client.go`, add:

```go
// WriteFile writes a file to a VM via the agent proxy's /write-file endpoint.
// Uses port 9091 (ProxyRouter) with X-Proxy-Token auth, matching handleMachineExec pattern.
func (c *Client) WriteFile(ctx context.Context, host *store.Host, machineID, proxyToken, path, content string) error {
	// Build proxy URL: port 9091 on the host (same as /exec proxy)
	var hostIP string
	if host.ExternalIP != nil {
		hostIP = *host.ExternalIP
	} else if host.InternalIP != nil {
		hostIP = *host.InternalIP
	} else {
		return fmt.Errorf("write-file: host has no reachable IP")
	}
	targetURL := fmt.Sprintf("http://%s:9091/proxy/%s/write-file", hostIP, machineID)
	body, _ := json.Marshal(map[string]string{"path": path, "content": content})
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Proxy-Token", proxyToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("write-file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("write-file: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
```

- [ ] **Step 3: Extend handlePushMachineConfig to write soul files before config push**

In `machine_config.go`, in `handlePushMachineConfig()`, after the `assembleConfigForMachine` call succeeds, but BEFORE the live config push to the agent (the section that starts `if machine.Status == "running"`), add soul writing:

```go
	// Write soul files BEFORE config push (souls must be on disk before gateway reload)
	if machine.Status == "running" && machine.HostID != nil {
		host, hostErr := s.store.GetHost(r.Context(), *machine.HostID)
		if hostErr == nil && machine.ProxyToken != nil {
			agents, agentErr := s.store.ListMachineAgents(r.Context(), machineID)
			if agentErr == nil {
				for _, a := range agents {
					if a.Soul == nil || *a.Soul == "" {
						continue
					}
					soulPath := "/home/openclaw/.openclaw/workspace-" + a.AgentID + "/SOUL.md"
					if a.IsDefault {
						soulPath = "/home/openclaw/.openclaw/workspace/SOUL.md"
					}
					if err := s.agentClient.WriteFile(r.Context(), host, machineID, *machine.ProxyToken, soulPath, *a.Soul); err != nil {
						slog.Warn("machine.config.soul_write_failed", "machine_id", machineID, "agent_id", a.AgentID, "error", err)
						warnings = append(warnings, fmt.Sprintf("soul write failed for agent %s: %v", a.AgentID, err))
					}
				}
			}
		}
	}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Clean build.

- [ ] **Step 5: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/machine_config.go backend/internal/agentclient/client.go
git commit -m "feat: extend config push to load agents and write soul files"
```

---

## Chunk 6: Cold-Start Soul Delivery

### Task 9: VM Create Payload + Metadata /v1/souls

**Files:**
- Modify: `backend/internal/metadata/metadata.go`
- Modify: `backend/internal/metadata/server_linux.go`
- Modify: `backend/internal/metadata/server_linux_test.go`
- Modify: `backend/internal/agentclient/client.go`
- Modify: `backend/internal/orchestrator/types.go`
- Modify: `backend/internal/orchestrator/firecracker_linux.go`
- Modify: `backend/internal/machines/runtime.go`

- [ ] **Step 1: Add SoulEntry type and Souls field to metadata**

In `metadata.go`, after the `CustomProviderConfig` struct (around line 67):

```go
// SoulEntry carries agent soul (personality) data for cold-start delivery.
type SoulEntry struct {
	AgentID string `json:"agent_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}
```

Add `Souls` field to `MachineConfig` struct (around line 54, with the other `json:"-"` fields):

```go
	Souls           []SoulEntry                `json:"-"` // served via /v1/souls, not /v1/config
```

- [ ] **Step 2: Add Souls to VMRequest and VMConfig**

In `agentclient/client.go`, add to `VMRequest` struct (around line 73):

```go
	Souls           []metadata.SoulEntry       `json:"souls,omitempty"`
```

In `orchestrator/types.go`, add to `VMConfig` struct (around line 68):

```go
	Souls            []metadata.SoulEntry
```

- [ ] **Step 3: Pass souls through orchestrator RegisterMachine**

In `orchestrator/firecracker_linux.go`, inside the `RegisterMachine` call (around line 235), add:

```go
			Souls:            cfg.Souls,
```

- [ ] **Step 4: Add /v1/souls metadata endpoint**

In `metadata/server_linux.go`, add to route registration (after `/v1/logs`, around line 37):

```go
	mux.HandleFunc("/v1/souls", s.handleSouls)
```

Add the handler:

```go
func (s *Server) handleSouls(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	souls := cfg.Souls
	if souls == nil {
		souls = []SoulEntry{}
	}
	_ = json.NewEncoder(w).Encode(souls)
}
```

- [ ] **Step 5: Write metadata test for /v1/souls**

In `metadata/server_linux_test.go`:

```go
func TestHandleSouls_ReturnsSoulFiles(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-1",
		Nonce:     "testnonce",
		Souls: []SoulEntry{
			{AgentID: "support", Path: "/home/openclaw/.openclaw/workspace/SOUL.md", Content: "You are a helper"},
			{AgentID: "creative", Path: "/home/openclaw/.openclaw/workspace-creative/SOUL.md", Content: "You are creative"},
		},
	})
	req := httptest.NewRequest("GET", "/v1/souls", nil)
	req.RemoteAddr = testVMIP + ":1234"
	req.Header.Set("X-Metadata-Nonce", "testnonce")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var souls []SoulEntry
	if err := json.Unmarshal(w.Body.Bytes(), &souls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(souls) != 2 {
		t.Fatalf("got %d souls, want 2", len(souls))
	}
	if souls[0].AgentID != "support" || souls[0].Content != "You are a helper" {
		t.Errorf("soul[0] = %+v", souls[0])
	}
}

func TestHandleSouls_EmptyWhenNoAgents(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-1",
		Nonce:     "testnonce",
	})
	req := httptest.NewRequest("GET", "/v1/souls", nil)
	req.RemoteAddr = testVMIP + ":1234"
	req.Header.Set("X-Metadata-Nonce", "testnonce")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var souls []SoulEntry
	_ = json.Unmarshal(w.Body.Bytes(), &souls)
	if len(souls) != 0 {
		t.Fatalf("got %d souls, want 0", len(souls))
	}
}
```

- [ ] **Step 6: Run metadata tests**

Run: `cd /home/mantiz/OpenClawMachines && go test ./backend/internal/metadata/ -v -run "TestHandleSouls"`
Expected: PASS

- [ ] **Step 7: Fetch agents in runtime.Start() and build souls for VMRequest**

In `machines/runtime.go`, inside `start()`, after credentials are prepared but before the `CreateVM` call (around line 420):

```go
	// Fetch agent personas for soul delivery via cold-start path
	var souls []metadata.SoulEntry
	if machineAgents, err := rs.store.ListMachineAgents(ctx, machine.ID); err == nil {
		for _, a := range machineAgents {
			if a.Soul == nil || *a.Soul == "" {
				continue
			}
			soulPath := "/home/openclaw/.openclaw/workspace-" + a.AgentID + "/SOUL.md"
			if a.IsDefault {
				soulPath = "/home/openclaw/.openclaw/workspace/SOUL.md"
			}
			souls = append(souls, metadata.SoulEntry{
				AgentID: a.AgentID,
				Path:    soulPath,
				Content: *a.Soul,
			})
		}
	}
```

Then add `souls` to the `CreateVM` call. The `CreateVM` function signature needs to accept the new parameter. In `agentclient/client.go`, add `souls []metadata.SoulEntry` parameter to `CreateVM` and set `req.Souls = souls` in the function body.

Update the call site in `runtime.go` to pass `souls`:

```go
	_, err := rs.agentClient.CreateVM(ctx, host, machine, vmIP, decryptedSecrets, llmKeys, channelKeys, rs.rootfsDataVersion, ownerEmails, rs.cfSSHCAPubKey, openClawConf, cpConfigs, browserVMIP, souls)
```

- [ ] **Step 8: Pass souls through handleCreateVM in agent**

In `backend/internal/agentapi/handlers.go`, in `handleCreateVM`, after extracting fields from `VMRequest` into `VMConfig`, add:

```go
	vmCfg.Souls = req.Souls
```

(The `VMConfig` struct was updated in step 2 to include `Souls`.)

- [ ] **Step 9: Verify full build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Clean build.

- [ ] **Step 10: Run all Go tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests PASS.

- [ ] **Step 11: Commit**

```bash
git add backend/internal/metadata/ backend/internal/agentclient/ backend/internal/orchestrator/ backend/internal/machines/runtime.go backend/internal/agentapi/handlers.go
git commit -m "feat: add cold-start soul delivery via VMRequest and /v1/souls"
```

---

### Task 10: Init Script Soul Fetch

**Files:**
- Modify: `scripts/init-openclaw.sh`

- [ ] **Step 1: Add soul file fetch after IDENTITY.md setup**

In `init-openclaw.sh`, after the IDENTITY.md block (around line 440), before the device identity section:

```bash
# ============================================
# 7b. Fetch and write soul files from metadata
# ============================================
phase_start "soul-files"
SOULS=$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/souls" 2>/dev/null)
if [ -n "$SOULS" ] && [ "$SOULS" != "null" ] && [ "$SOULS" != "[]" ]; then
    SOUL_COUNT=$(echo "$SOULS" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    if [ "$SOUL_COUNT" -gt 0 ]; then
        echo "$SOULS" | python3 -c "
import json, sys, os
for s in json.load(sys.stdin):
    p = s['path']
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, 'w') as f:
        f.write(s['content'])
    os.chown(p, 1000, 1000)
    print(f'  [OK] Wrote {p}')
" 2>/dev/null
        echo "  Wrote $SOUL_COUNT soul file(s)"
    else
        echo "  No soul files to write"
    fi
else
    echo "  No soul files from metadata"
fi
phase_end "soul-files"
```

- [ ] **Step 2: Commit**

```bash
git add scripts/init-openclaw.sh
git commit -m "feat: fetch and write soul files in init script before gateway start"
```

---

## Chunk 7: Agent-Auth Routes + Metadata Admin

### Task 11: Agent-Token-Authenticated Backend Routes

**Files:**
- Create: `backend/internal/api/agent_auth.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Create agent_auth.go with token-based auth handlers**

```go
package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// authenticateAgentToken validates the bearer token against per-host or fleet-wide tokens.
// Returns the host ID on success, or writes an error response and returns 0 on failure.
func (s *Server) authenticateAgentToken(w http.ResponseWriter, r *http.Request) (int, bool) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return 0, false
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Try per-host token lookup
	host, err := s.store.GetHostByAgentToken(r.Context(), token)
	if err == nil && host != nil {
		return host.ID, true
	}

	// Fall back to fleet-wide token
	if s.agentToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.agentToken)) == 1 {
		return 0, true // fleet-wide auth, no specific host
	}

	writeError(w, http.StatusUnauthorized, "invalid agent token")
	return 0, false
}

// validateMachinePlacement verifies that a machine is currently placed on the given host.
// For fleet-wide tokens (hostID=0), the check is skipped.
func (s *Server) validateMachinePlacement(ctx context.Context, machineID string, hostID int) error {
	if hostID == 0 {
		return nil // fleet-wide token, no host-specific check
	}
	machine, err := s.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("machine not found")
	}
	if machine.HostID == nil || *machine.HostID != hostID {
		return fmt.Errorf("machine is not placed on this host")
	}
	return nil
}

func (s *Server) handleAgentAuthListAgents(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")

	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	agents, err := s.store.ListMachineAgents(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleAgentAuthCreateAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")

	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req struct {
		AgentID        string  `json:"agent_id"`
		Name           string  `json:"name"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      bool    `json:"is_default"`
		SortOrder      int     `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "agent_id and name are required")
		return
	}
	if !validAgentID.MatchString(req.AgentID) {
		writeError(w, http.StatusBadRequest, "agent_id must be lowercase alphanumeric with hyphens")
		return
	}
	if err := s.validateAgentModel(r.Context(), machineID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agent := &store.MachineAgent{
		MachineID:      machineID,
		AgentID:        req.AgentID,
		Name:           req.Name,
		Model:          req.Model,
		IdentityEmoji:  req.IdentityEmoji,
		IdentityAvatar: req.IdentityAvatar,
		Soul:           req.Soul,
		IsDefault:      req.IsDefault,
		SortOrder:      req.SortOrder,
	}
	if err := s.store.CreateMachineAgent(r.Context(), agent); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "agent_id already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("machine.agent.created.via_agent_auth", "machine_id", machineID, "agent_id", req.AgentID)
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleAgentAuthUpdateAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")
	agentID := chi.URLParam(r, "agentId")

	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	existing, err := s.store.GetMachineAgent(r.Context(), machineID, agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	var req struct {
		Name           *string `json:"name,omitempty"`
		Model          *string `json:"model,omitempty"`
		IdentityEmoji  *string `json:"identity_emoji,omitempty"`
		IdentityAvatar *string `json:"identity_avatar,omitempty"`
		Soul           *string `json:"soul,omitempty"`
		IsDefault      *bool   `json:"is_default,omitempty"`
		SortOrder      *int    `json:"sort_order,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.validateAgentModel(r.Context(), machineID, req.Model); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Name != nil { existing.Name = *req.Name }
	if req.Model != nil { existing.Model = req.Model }
	if req.IdentityEmoji != nil { existing.IdentityEmoji = req.IdentityEmoji }
	if req.IdentityAvatar != nil { existing.IdentityAvatar = req.IdentityAvatar }
	if req.Soul != nil { existing.Soul = req.Soul }
	if req.IsDefault != nil { existing.IsDefault = *req.IsDefault }
	if req.SortOrder != nil { existing.SortOrder = *req.SortOrder }

	if err := s.store.UpdateMachineAgent(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthDeleteAgent(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")
	agentID := chi.URLParam(r, "agentId")

	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	if err := s.store.DeleteMachineAgent(r.Context(), machineID, agentID); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentAuthPushConfig(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}
	machineID := chi.URLParam(r, "machineID")

	// Validate machine belongs to this host (placement check)
	if err := s.validateMachinePlacement(r.Context(), machineID, hostID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	// Delegate to the shared pushMachineConfigInternal helper.
	// This is the same logic used by handlePushMachineConfig (dashboard path),
	// ensuring soul file writes, config assembly, and live updates are consistent.
	// The implementer should extract pushMachineConfigInternal() from
	// handlePushMachineConfig (machine_config.go:420-492) so both handlers
	// call the same code. The helper signature:
	//   func (s *Server) pushMachineConfigInternal(ctx context.Context, machineID string) (map[string]interface{}, error)
	// Returns the response payload map on success.
	s.pushMachineConfigInternal(w, r, machineID)
}
```

- [ ] **Step 2: Register agent-auth routes in server.go**

After the existing agent endpoints (around line 425):

```go
	// Agent-authenticated machine operations (called by host agent's metadata server)
	r.Get("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthListAgents)
	r.Post("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthCreateAgent)
	r.Put("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthUpdateAgent)
	r.Delete("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthDeleteAgent)
	r.Post("/api/agent/machines/{machineID}/config/push", srv.handleAgentAuthPushConfig)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/internal/api/...`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/agent_auth.go backend/internal/api/server.go
git commit -m "feat: add agent-token-authenticated routes for self-config skill"
```

---

### Task 12: Metadata Server Admin Proxy Endpoints

**Files:**
- Modify: `backend/internal/metadata/metadata.go`
- Modify: `backend/internal/metadata/server_linux.go`

- [ ] **Step 1: Add BackendURL and AgentToken fields to metadata Server**

In `metadata.go`, add to the `Server` struct (around line 23):

```go
	BackendURL  string // backend API URL for admin proxy
	AgentToken  string // for authenticating to backend API
```

- [ ] **Step 2: Add /v1/admin/* proxy handler**

In `server_linux.go`, add route (after /v1/souls):

```go
	mux.HandleFunc("/v1/admin/", s.handleAdminProxy)
```

Add the handler:

```go
// handleAdminProxy proxies admin requests to the backend API for self-config.
func (s *Server) handleAdminProxy(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}
	if s.BackendURL == "" || s.AgentToken == "" {
		http.Error(w, "admin proxy not configured", http.StatusServiceUnavailable)
		return
	}

	// Strip /v1/admin/ prefix and build backend URL
	// /v1/admin/agents → /api/agent/machines/{machineID}/agents
	subPath := strings.TrimPrefix(r.URL.Path, "/v1/admin/")
	targetURL := fmt.Sprintf("%s/api/agent/machines/%s/%s", s.BackendURL, cfg.MachineID, subPath)

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Authorization", "Bearer "+s.AgentToken)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "backend unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

Add required imports: `"strings"`, `"time"`, `"io"`.

- [ ] **Step 3: Wire BackendURL and AgentToken in agent main.go**

In `backend/cmd/agent/main.go`, where the metadata server is created (around line 129):

```go
	metaSrv := metadata.New(bridgeGateway, 80)
	metaSrv.BackendURL = cfg.BackendURL
	metaSrv.AgentToken = cfg.AgentToken
```

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Clean build.

- [ ] **Step 5: Run all Go tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/metadata/ backend/cmd/agent/main.go
git commit -m "feat: add metadata server /v1/admin/* proxy for self-config skill"
```

---

## Chunk 8: Frontend Dashboard UI

### Task 13: Frontend Agent API + Components

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Create: `frontend/src/components/MachineAgents.tsx`

- [ ] **Step 1: Add agent API functions to api.ts**

```typescript
// Agent (persona) management
export interface MachineAgent {
  id: string;
  machine_id: string;
  agent_id: string;
  name: string;
  model?: string;
  identity_emoji?: string;
  identity_avatar?: string;
  soul?: string;
  is_default: boolean;
  sort_order: number;
  created_at: string;
  updated_at: string;
}

export interface CreateAgentRequest {
  agent_id: string;
  name: string;
  model?: string;
  identity_emoji?: string;
  identity_avatar?: string;
  soul?: string;
  is_default?: boolean;
  sort_order?: number;
}

export const listMachineAgents = (accountId: number, machineId: string) =>
  request<MachineAgent[]>(`/accounts/${accountId}/machines/${machineId}/agents`);

export const createMachineAgent = (accountId: number, machineId: string, data: CreateAgentRequest) =>
  request<MachineAgent>(`/accounts/${accountId}/machines/${machineId}/agents`, {
    method: "POST",
    body: JSON.stringify(data),
  });

export const updateMachineAgent = (accountId: number, machineId: string, agentId: string, data: Partial<CreateAgentRequest>) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/agents/${agentId}`, {
    method: "PUT",
    body: JSON.stringify(data),
  });

export const deleteMachineAgent = (accountId: number, machineId: string, agentId: string) =>
  request<{ status: string }>(`/accounts/${accountId}/machines/${machineId}/agents/${agentId}`, {
    method: "DELETE",
  });
```

- [ ] **Step 2: Create MachineAgents.tsx component**

Build a component that:
- Lists agents as cards with name, emoji, model
- Has a "Create Agent" button opening a dialog (Radix Dialog)
- Each agent card has edit/delete actions
- Edit opens a dialog with pre-populated fields
- Delete shows a confirmation dialog
- Default agent shows a badge and cannot be deleted

Follow existing Radix + Tailwind patterns in the codebase.

> **Note:** The exact component code depends on the existing dashboard page structure. The implementer should read the machine detail page (likely `frontend/src/pages/MachineDetail.tsx` or similar) to understand where the agent list should be placed and what context providers are available (accountId, machineId).

- [ ] **Step 3: Verify frontend builds**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: No type errors.

- [ ] **Step 4: Run frontend tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-frontend`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/components/MachineAgents.tsx
git commit -m "feat: add agent management UI to dashboard"
```

---

## Chunk 9: Final Integration + CurrentFeature Update

### Task 14: Update CurrentFeature.md

**Files:**
- Modify: `docs/CurrentFeature.md`

- [ ] **Step 1: Check off Agent CRUD item**

Update the "Next: Managed Extensions" section to check off the agent CRUD item:

```
- [x] Agent CRUD in the control plane (agents are the most common user-authored entity)
```

- [ ] **Step 2: Run full test suite**

Run: `cd /home/mantiz/OpenClawMachines && make test-go && make test-frontend`
Expected: All tests PASS.

- [ ] **Step 3: Commit**

```bash
git add docs/CurrentFeature.md
git commit -m "docs: check off agent CRUD in CurrentFeature"
```

---

## Execution Notes

### Implementation Notes

1. **Migration number**: Verify `036` is still the next available number at execution time. If another migration was added, bump accordingly.

2. **Push handler refactoring (Task 8 + Task 11)**: Task 8 extends `handlePushMachineConfig` with soul writing. Task 11's `handleAgentAuthPushConfig` must call the same logic. Extract a shared `pushMachineConfigInternal(ctx, machineID string) (map[string]interface{}, error)` helper from `handlePushMachineConfig` that both handlers call. This ensures soul writes, config assembly, store updates, and live pushes are consistent across both the dashboard and self-config paths.

3. **python3 in rootfs**: The init script uses `python3` for JSON parsing of soul files. Verify python3 is available in the rootfs image. If not, use `jq` or a shell-only approach.

4. **CreateVM signature**: The `CreateVM` function already has 13 positional parameters. Adding `souls` makes it 14. The implementer should carefully match parameter order. Consider using a struct-based options pattern in the future, but for now keep it consistent with existing style.

### Test Commands Reference
| Suite | Command | Duration |
|-------|---------|----------|
| All Go tests | `make test-go` | ~20s |
| Config assembly only | `go test ./backend/internal/configassembly/ -v` | ~2s |
| Metadata server only | `go test ./backend/internal/metadata/ -v` | ~2s |
| Gateway E2E | `make test-gateway-e2e` | ~12s |
| Frontend tests | `make test-frontend` | ~10s |
| Frontend typecheck | `make typecheck` | ~5s |

### Task Dependencies
```
Task 1 (migration) → Task 2 (store types) → Task 3 (store impl)
                                            ↓
Task 4 (config assembly) ─────────────────→ Task 8 (config push)
                                            ↑
Task 5 (API handlers) ────────────────────→ Task 8
Task 6 (PTY /write-file) → Task 7 (proxy) → Task 8
Task 9 (cold-start) depends on Task 3, Task 4
Task 10 (init script) depends on Task 9
Task 11 (agent-auth) depends on Task 3, Task 5
Task 12 (metadata admin) depends on Task 11
Task 13 (frontend) depends on Task 5
Task 14 (docs) is last
```

### Independent Parallelization Opportunities
These tasks can run in parallel after Task 3 completes:
- **Task 4** (config assembly) — no API dependency
- **Task 6** (PTY /write-file) — no store dependency
- **Task 5** (API handlers) — needs store types only

After Task 5 completes:
- **Task 11** (agent-auth) — parallel with Task 8
- **Task 13** (frontend) — parallel with Task 8
