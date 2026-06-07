package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// PlacementRequest describes the resources needed for a machine placement.
type PlacementRequest struct {
	VCPUs         int
	MemoryMB      int
	DiskMB        int
	Region        string
	ExpectedImage string
	OperationID   string
	TargetHostID  int // If non-zero, place on this specific host (admin migration)

	// RequireBrowserReflink restricts browser VM placement to hosts whose
	// browser state storage can satisfy cp --reflink=always.
	RequireBrowserReflink bool
}

// ReleaseMode controls how a placement is released.
type ReleaseMode int

const (
	// ReleaseSoft frees vCPU/memory counters but preserves home_host_id for affinity.
	// Disk is NOT released — the data volume stays on the host.
	ReleaseSoft ReleaseMode = iota
	// ReleaseHard frees everything including host_id, vm_ip, home_host_id, and disk.
	ReleaseHard
)

// Strategy controls how eligible hosts are ranked for placement.
type Strategy int

const (
	// Random shuffles eligible hosts (default). Best for identical hosts in a region.
	Random Strategy = iota
	// Spread prefers the least-loaded host (by memory).
	Spread
	// BinPack prefers the most-loaded host (by memory).
	BinPack
)

// DefaultDiskPerVM is the default disk allocation per VM when not specified.
// 3 GB rootfs (CoW) + 5 GB data volume = ~8 GB.
const DefaultDiskPerVM = 8192 // MB

const hostPlacementHeartbeatMaxAge = 180 * time.Second

// PlacementStore is the subset of store interfaces needed by PlacementService.
type PlacementStore interface {
	store.PlacementRepo
	store.HostRepo
	store.MachineRepo
	store.BrowserVMRepo
}

// PlacementConfig holds configuration for the placement service.
type PlacementConfig struct {
	DefaultRegion    string
	ExpectedImage    string
	Strategy         Strategy
	DefaultDiskPerVM int // MB, used when machine spec doesn't specify disk
}

// PlacementService manages machine-to-host placement with placement records.
type PlacementService struct {
	store  PlacementStore
	config PlacementConfig
}

// NewPlacementService creates a new PlacementService.
func NewPlacementService(s PlacementStore, config PlacementConfig) *PlacementService {
	if config.DefaultDiskPerVM == 0 {
		config.DefaultDiskPerVM = DefaultDiskPerVM
	}
	return &PlacementService{
		store:  s,
		config: config,
	}
}

// ExpectedImage returns the default source image used for placement matching.
func (ps *PlacementService) ExpectedImage() string { return ps.config.ExpectedImage }

// Reserve finds an eligible host, places the machine, and creates a placement record.
// If req.TargetHostID is set, places on that specific host instead of auto-selecting.
func (ps *PlacementService) Reserve(ctx context.Context, machineID string, req PlacementRequest) (*store.Placement, *store.Host, string, error) {
	if req.TargetHostID != 0 {
		return ps.placeOnSpecificHost(ctx, machineID, req)
	}
	return ps.placeAutoSelect(ctx, machineID, req)
}

func (ps *PlacementService) placeAutoSelect(ctx context.Context, machineID string, req PlacementRequest) (*store.Placement, *store.Host, string, error) {
	diskMB := req.DiskMB
	if diskMB == 0 {
		diskMB = ps.config.DefaultDiskPerVM
	}

	region := req.Region
	if region == "" {
		region = ps.config.DefaultRegion
	}
	// Image filter removed: with decoupled artifacts, VMs pull rootfs from GCS
	// at start time. Host source_image no longer needs to match.

	// 1. Get eligible hosts (read-only, no locks)
	hosts, err := ps.store.ListEligibleHosts(ctx, store.HostFilter{
		MinVCPUs:    req.VCPUs,
		MinMemoryMB: req.MemoryMB,
		MinDiskMB:   diskMB,
		Region:      region,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("list eligible hosts: %w", err)
	}
	if len(hosts) == 0 {
		return nil, nil, "", fmt.Errorf("%w in region %q", store.ErrNoEligibleHost, region)
	}

	// 2. Rank by strategy (pure Go — unit testable)
	ranked := ps.rank(hosts)

	// 3. Try hosts in ranked order — PlaceOnHost is atomic per attempt
	for _, host := range ranked {
		vmIP, err := ps.store.PlaceOnHost(ctx, host.ID, machineID, req.VCPUs, req.MemoryMB, diskMB)
		if err != nil {
			slog.Warn("placement.attempt_failed", "host_id", host.ID, "error", err)
			continue // contended or capacity changed — try next
		}

		placement, err := ps.store.ReservePlacement(ctx, machineID, host.ID, vmIP, req.OperationID)
		if err != nil {
			slog.Error("placement.reserve.record_failed", "machine_id", machineID, "error", err)
			return nil, &host, vmIP, nil
		}

		slog.Info("placement.reserved", "machine_id", machineID, "host_id", host.ID, "vm_ip", vmIP, "placement_id", placement.ID)
		return placement, &host, vmIP, nil
	}

	return nil, nil, "", fmt.Errorf("%w: all %d eligible hosts at capacity or contended", store.ErrNoEligibleHost, len(ranked))
}

func (ps *PlacementService) placeOnSpecificHost(ctx context.Context, machineID string, req PlacementRequest) (*store.Placement, *store.Host, string, error) {
	diskMB := req.DiskMB
	if diskMB == 0 {
		diskMB = ps.config.DefaultDiskPerVM
	}

	vmIP, err := ps.store.PlaceOnHost(ctx, req.TargetHostID, machineID, req.VCPUs, req.MemoryMB, diskMB)
	if err != nil {
		return nil, nil, "", fmt.Errorf("target host %d: %w", req.TargetHostID, err)
	}

	host, err := ps.store.GetHost(ctx, req.TargetHostID)
	if err != nil {
		// Roll back capacity allocated by PlaceOnHost since we can't proceed.
		if rbErr := ps.store.ReleaseHostCapacityWithDisk(ctx, req.TargetHostID, req.VCPUs, req.MemoryMB, diskMB); rbErr != nil {
			slog.Error("placement.rollback_failed", "host_id", req.TargetHostID, "error", rbErr)
		}
		return nil, nil, "", fmt.Errorf("get target host %d after placement: %w", req.TargetHostID, err)
	}
	placement, err := ps.store.ReservePlacement(ctx, machineID, req.TargetHostID, vmIP, req.OperationID)
	if err != nil {
		slog.Error("placement.reserve.record_failed", "machine_id", machineID, "error", err)
		return nil, host, vmIP, nil
	}

	slog.Info("placement.reserved", "machine_id", machineID, "host_id", req.TargetHostID, "vm_ip", vmIP)
	return placement, host, vmIP, nil
}

// ReserveBrowserVM picks a host and reserves capacity for a browser VM.
// If req.TargetHostID is non-zero, places directly on that host instead of auto-selecting.
func (ps *PlacementService) ReserveBrowserVM(ctx context.Context, browserVMID string, req PlacementRequest) (*store.Host, string, error) {
	if req.TargetHostID != 0 {
		host, err := ps.store.GetHost(ctx, req.TargetHostID)
		if err != nil {
			return nil, "", fmt.Errorf("get target host %d before browser placement: %w", req.TargetHostID, err)
		}
		if req.RequireBrowserReflink && !hostBrowserReflinkSupported(host) {
			return nil, "", fmt.Errorf("target host %d does not report reflink-capable browser storage", req.TargetHostID)
		}
		vmIP, err := ps.store.PlaceBrowserVMOnHost(ctx, req.TargetHostID, browserVMID, req.VCPUs, req.MemoryMB)
		if err != nil {
			return nil, "", fmt.Errorf("target host %d: %w", req.TargetHostID, err)
		}
		host, err = ps.store.GetHost(ctx, req.TargetHostID)
		if err != nil {
			if rbErr := ps.store.ReleaseBrowserVMPlacement(ctx, browserVMID); rbErr != nil {
				slog.Error("browser_vm.placement.rollback_failed", "browser_vm_id", browserVMID, "host_id", req.TargetHostID, "error", rbErr)
			}
			return nil, "", fmt.Errorf("get target host %d after browser placement: %w", req.TargetHostID, err)
		}
		return host, vmIP, nil
	}

	region := req.Region
	if region == "" {
		region = ps.config.DefaultRegion
	}

	hosts, err := ps.store.ListEligibleHosts(ctx, store.HostFilter{
		MinVCPUs:              req.VCPUs,
		MinMemoryMB:           req.MemoryMB,
		Region:                region,
		RequireBrowserReflink: req.RequireBrowserReflink,
	})
	if err != nil {
		return nil, "", fmt.Errorf("list eligible hosts: %w", err)
	}
	if len(hosts) == 0 {
		return nil, "", fmt.Errorf("no eligible hosts for browser VM")
	}

	ranked := ps.rank(hosts)
	for _, host := range ranked {
		if req.RequireBrowserReflink && !hostBrowserReflinkSupported(&host) {
			slog.Warn("browser_vm.placement.skip_host_without_reflink", "host_id", host.ID)
			continue
		}
		vmIP, err := ps.store.PlaceBrowserVMOnHost(ctx, host.ID, browserVMID, req.VCPUs, req.MemoryMB)
		if err != nil {
			slog.Warn("browser_vm.placement.attempt_failed", "host_id", host.ID, "error", err)
			continue
		}
		return &host, vmIP, nil
	}
	return nil, "", fmt.Errorf("all eligible hosts exhausted for browser VM")
}

func hostBrowserReflinkSupported(host *store.Host) bool {
	if host == nil || len(host.Capabilities) == 0 {
		return false
	}
	var caps struct {
		BrowserStorage *struct {
			ReflinkSupported bool `json:"reflink_supported"`
		} `json:"browser_storage"`
		Storage *struct {
			ReflinkSupported bool `json:"reflink_supported"`
		} `json:"storage"`
	}
	if err := json.Unmarshal(host.Capabilities, &caps); err != nil {
		return false
	}
	if caps.BrowserStorage != nil {
		return caps.BrowserStorage.ReflinkSupported
	}
	if caps.Storage != nil {
		return caps.Storage.ReflinkSupported
	}
	return false
}

// rank orders hosts by placement strategy. Pure function — unit testable.
func (ps *PlacementService) rank(hosts []store.Host) []store.Host {
	out := make([]store.Host, len(hosts))
	copy(out, hosts)
	switch ps.config.Strategy {
	case Random:
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	case Spread:
		sort.Slice(out, func(i, j int) bool {
			return out[i].UsedMemoryMB < out[j].UsedMemoryMB
		})
	case BinPack:
		sort.Slice(out, func(i, j int) bool {
			return out[i].UsedMemoryMB > out[j].UsedMemoryMB
		})
	}
	return out
}

// Activate marks a placement as active (VM confirmed running).
func (ps *PlacementService) Activate(ctx context.Context, placementID string) error {
	ok, err := ps.store.ActivatePlacement(ctx, placementID)
	if err != nil {
		return fmt.Errorf("activate placement: %w", err)
	}
	if !ok {
		return fmt.Errorf("placement %s not in reserved state", placementID)
	}
	slog.Info("placement.activated", "placement_id", placementID)
	return nil
}

// Release releases a machine's placement.
// ReleaseSoft: free vCPU/memory counters, set home_host_id, release placement record.
//
//	Disk is NOT released (data volume stays on host).
//
// ReleaseHard: free all counters (including disk), clear host_id/vm_ip/home_host_id.
func (ps *PlacementService) Release(ctx context.Context, machineID string, mode ReleaseMode) error {
	machine, err := ps.store.GetMachine(ctx, machineID)
	if err != nil {
		return fmt.Errorf("get machine: %w", err)
	}

	// Release placement record
	_ = ps.store.ReleasePlacement(ctx, machineID)

	if machine.HostID == nil {
		return nil
	}
	hostID := *machine.HostID

	// Derive disk usage from machine's data volume + rootfs overhead.
	// DataVolumeGB is the user-facing data volume size; add ~3 GB for rootfs.
	diskMB := ps.config.DefaultDiskPerVM
	if machine.DataVolumeGB > 0 {
		diskMB = (machine.DataVolumeGB + 3) * 1024
	}

	switch mode {
	case ReleaseSoft:
		// Free vCPU/memory but keep host_id/vm_ip and disk (data volume persists)
		if err := ps.store.SoftStopMachine(ctx, machineID, hostID, machine.VCPUs, machine.MemoryMB, diskMB); err != nil {
			return fmt.Errorf("soft stop: %w", err)
		}
		// Set home_host_id for future affinity
		_ = ps.store.UpdateMachineHomeHost(ctx, machineID, machine.HostID)
		slog.Info("placement.released.soft", "machine_id", machineID, "host_id", hostID)

	case ReleaseHard:
		// Free all counters including disk
		if err := ps.store.ReleaseHostCapacityWithDisk(ctx, hostID, machine.VCPUs, machine.MemoryMB, diskMB); err != nil {
			return fmt.Errorf("release capacity: %w", err)
		}
		// Clear all host references
		if err := ps.store.UnassignMachineFromHost(ctx, machineID); err != nil {
			return fmt.Errorf("unassign: %w", err)
		}
		_ = ps.store.UpdateMachineHomeHost(ctx, machineID, nil)
		slog.Info("placement.released.hard", "machine_id", machineID, "host_id", hostID)
	}

	return nil
}

// RecoverAffinity checks if the machine's home host is viable for restart.
// If the home host cannot accept the machine (capacity full, unreachable, draining, error),
// it returns an error — silent fresh placement would cause data loss (data volume stays on home host).
// Exception: "terminated" hosts are handled in runtime.go before RecoverAffinity is called.
func (ps *PlacementService) RecoverAffinity(ctx context.Context, machineID string, req PlacementRequest) (*store.Placement, *store.Host, string, error) {
	machine, err := ps.store.GetMachine(ctx, machineID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get machine: %w", err)
	}

	var homeHostID *int
	if machine.HomeHostID != nil {
		homeHostID = machine.HomeHostID
	} else if machine.HostID != nil {
		homeHostID = machine.HostID
	}

	if homeHostID == nil {
		return nil, nil, "", fmt.Errorf("machine has no home host — cannot recover affinity")
	}

	host, err := ps.store.GetHost(ctx, *homeHostID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("home host lookup failed: %w", err)
	}

	if err := validateHostPlacementEligibility(*host, time.Now()); err != nil {
		return nil, nil, "", fmt.Errorf("home host %d is not eligible — migration required: %w", host.ID, err)
	}

	diskMB := req.DiskMB
	if diskMB == 0 {
		diskMB = ps.config.DefaultDiskPerVM
	}

	// Re-allocate vCPU/memory (disk stays allocated from soft stop).
	// If the stopped machine released its VM IP, the store allocates a fresh
	// host-local IP while holding the host lock.
	vmIP, err := ps.store.ReAllocateHostCapacity(ctx, machineID, host.ID, req.VCPUs, req.MemoryMB, diskMB)
	if err != nil {
		return nil, nil, "", fmt.Errorf("home host %d has insufficient capacity — migration required: %w", host.ID, err)
	}

	placement, _ := ps.store.ReservePlacement(ctx, machineID, host.ID, vmIP, req.OperationID)
	slog.Info("placement.affinity_reused", "machine_id", machineID, "host_id", host.ID)
	return placement, host, vmIP, nil
}

func validateHostPlacementEligibility(host store.Host, now time.Time) error {
	if host.Status != "ready" {
		return fmt.Errorf("host is %s, not ready", host.Status)
	}
	if host.MaintenanceMode {
		return fmt.Errorf("host is in maintenance mode")
	}
	if host.LastHeartbeat == nil {
		return fmt.Errorf("host has no heartbeat")
	}
	if host.LastHeartbeat.Before(now.Add(-hostPlacementHeartbeatMaxAge)) {
		return fmt.Errorf("host heartbeat is stale")
	}
	return nil
}
