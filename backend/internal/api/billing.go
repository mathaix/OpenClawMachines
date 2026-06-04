package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- Budget handlers ----

func (s *Server) handleSetBudget(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

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
		LimitCents int64 `json:"limit_cents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.LimitCents <= 0 {
		writeError(w, http.StatusBadRequest, "limit_cents must be a positive integer")
		return
	}

	budgetMicrocents := req.LimitCents * 10000

	if err := s.store.SetMachineBudget(r.Context(), machineID, budgetMicrocents); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set budget")
		return
	}

	machine.BudgetMicrocents = &budgetMicrocents
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine_id":        machineID,
		"budget_microcents": budgetMicrocents,
		"limit_cents":       req.LimitCents,
	})

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "billing",
		Action:    "billing.budget_set",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Set $%.2f budget on '%s'", float64(req.LimitCents)/100, machine.Name),
		Detail:    map[string]any{"limit_cents": req.LimitCents},
	})
}

func (s *Server) handleDeleteBudget(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	if err := s.store.ClearMachineBudget(r.Context(), machineID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear budget")
		return
	}

	w.WriteHeader(http.StatusNoContent)

	s.activity.Log(r.Context(), events.LogParams{
		Category:  "billing",
		Action:    "billing.budget_cleared",
		Status:    "success",
		ActorType: "user",
		ActorID:   userIDFromContext(r.Context()),
		AccountID: &accountID,
		MachineID: &machineID,
		Summary:   fmt.Sprintf("Cleared budget on '%s'", machine.Name),
	})
}

// ---- Usage handlers ----

func (s *Server) handleGetAccountUsage(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())

	// Get all machines for this account
	machines, err := s.store.ListMachinesByAccount(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list machines")
		return
	}

	// Build per-machine spend breakdown
	type machineSpend struct {
		MachineID        string `json:"machine_id"`
		MachineName      string `json:"machine_name"`
		SpendMicrocents  int64  `json:"spend_microcents"`
		BudgetMicrocents *int64 `json:"budget_microcents,omitempty"`
	}

	var totalSpend int64
	perMachine := make([]machineSpend, 0, len(machines))

	for _, m := range machines {
		spend, err := s.store.GetOpikSpendByMachine(r.Context(), accountID, m.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get machine spend")
			return
		}
		totalSpend += spend
		perMachine = append(perMachine, machineSpend{
			MachineID:        m.ID,
			MachineName:      m.Name,
			SpendMicrocents:  spend,
			BudgetMicrocents: m.BudgetMicrocents,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"account_id":          accountID,
		"current_month_spend": totalSpend,
		"per_machine":         perMachine,
	})
}

func (s *Server) handleGetMachineUsage(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	// Parse ?since= query param (default: start of current month)
	since := time.Now().UTC().Truncate(24 * time.Hour)
	since = time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, time.UTC)
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		parsed, err := time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter: use RFC3339 format")
			return
		}
		since = parsed
	}

	// Get current month spend
	spend, err := s.store.GetOpikSpendByMachine(r.Context(), accountID, machineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get machine spend")
		return
	}

	// Get detailed usage records
	records, err := s.store.GetOpikUsageByMachine(r.Context(), accountID, machineID, since, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage records")
		return
	}

	// Ensure we return an empty array rather than null
	if records == nil {
		records = []store.LLMUsage{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"machine_id":          machineID,
		"current_month_spend": spend,
		"budget_microcents":   machine.BudgetMicrocents,
		"since":               since.Format(time.RFC3339),
		"records":             records,
	})
}

func (s *Server) handleGetMachineUsageBreakdown(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromContext(r.Context())
	machineID := chi.URLParam(r, "id")

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}
	if machine.AccountID != accountID {
		writeError(w, http.StatusForbidden, "machine does not belong to this account")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "hour"
	}
	if period != "hour" && period != "day" {
		writeError(w, http.StatusBadRequest, "period must be 'hour' or 'day'")
		return
	}

	var since time.Time
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		parsed, err := time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter: use RFC3339 format")
			return
		}
		since = parsed
	} else {
		now := time.Now().UTC()
		if period == "hour" {
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}

	buckets, err := s.store.GetOpikUsageBreakdown(r.Context(), accountID, machineID, period, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage breakdown")
		return
	}
	if buckets == nil {
		buckets = []store.UsageBucket{}
	}

	var totalIn, totalOut int
	var totalCost int64
	var totalReqs int
	for _, b := range buckets {
		for _, e := range b.Entries {
			totalIn += e.InputTokens
			totalOut += e.OutputTokens
			totalCost += e.CostMicrocents
			totalReqs += e.RequestCount
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"period":  period,
		"since":   since.Format(time.RFC3339),
		"buckets": buckets,
		"totals": map[string]interface{}{
			"input_tokens":    totalIn,
			"output_tokens":   totalOut,
			"cost_microcents": totalCost,
			"request_count":   totalReqs,
		},
	})
}
