package provisioner

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/agentclient"
	"github.com/mathaix/openclawmachines/backend/internal/events"
	"github.com/mathaix/openclawmachines/backend/internal/store"
	"github.com/mathaix/openclawmachines/backend/internal/tunnel"
	"github.com/mathaix/openclawmachines/backend/pkg/version"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"
)

// hostStartupScript bootstraps a stock Ubuntu host on first boot when no
// prebaked image (snapshot) is configured.
//
//go:embed host-startup.sh
var hostStartupScript string

// defaultHostImage is the stock image booted when no snapshot is set.
const defaultHostImage = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64"

// Provisioner manages Host VM lifecycle — creating, configuring, and draining Hosts.
type Provisioner struct {
	store                    store.Store
	agentClient              *agentclient.Client
	tunnel                   *tunnel.Manager
	activity                 *events.Activity
	project                  string
	zone                     string
	region                   string
	snapshot                 string
	hostImage                string
	artifactBucket           string
	artifactBaseURL          string
	firewallEnsured          bool
	provisioningModel        string
	agentToken               string
	backendURL               string
	dataPlaneDomain          string
	dataDiskSizeGB           int64
	rootfsGCSManifest        string
	agentGCSManifest         string
	browserRootfsGCSManifest string
	browserRootfsVersion     string
	backupMasterKey          string
}

// Config holds the configuration for the Provisioner.
type Config struct {
	Store                    store.Store
	AgentClient              *agentclient.Client
	Tunnel                   *tunnel.Manager
	Project                  string
	Zone                     string
	Region                   string
	Snapshot                 string
	HostImage                string
	ArtifactBucket           string
	ArtifactBaseURL          string
	ProvisioningModel        string
	AgentToken               string
	BackendURL               string
	DataPlaneDomain          string
	DataDiskSizeGB           int64
	RootfsGCSManifest        string
	AgentGCSManifest         string
	BrowserRootfsGCSManifest string
	BrowserRootfsVersion     string
	BackupMasterKey          string
}

func New(cfg Config) *Provisioner {
	p := &Provisioner{
		store:                    cfg.Store,
		agentClient:              cfg.AgentClient,
		tunnel:                   cfg.Tunnel,
		project:                  cfg.Project,
		zone:                     cfg.Zone,
		region:                   cfg.Region,
		snapshot:                 cfg.Snapshot,
		hostImage:                strings.TrimSpace(cfg.HostImage),
		artifactBucket:           strings.TrimRight(strings.TrimSpace(cfg.ArtifactBucket), "/"),
		artifactBaseURL:          strings.TrimRight(strings.TrimSpace(cfg.ArtifactBaseURL), "/"),
		provisioningModel:        cfg.ProvisioningModel,
		agentToken:               cfg.AgentToken,
		backendURL:               cfg.BackendURL,
		dataPlaneDomain:          strings.Trim(strings.TrimSpace(cfg.DataPlaneDomain), "."),
		dataDiskSizeGB:           cfg.DataDiskSizeGB,
		rootfsGCSManifest:        cfg.RootfsGCSManifest,
		agentGCSManifest:         cfg.AgentGCSManifest,
		browserRootfsGCSManifest: cfg.BrowserRootfsGCSManifest,
		browserRootfsVersion:     cfg.BrowserRootfsVersion,
		backupMasterKey:          cfg.BackupMasterKey,
	}
	// dataDiskSizeGB == 0 means "auto-size based on host capacity" (see diskSizes)
	return p
}

// SetActivity configures the activity logger for emitting host lifecycle events.
func (p *Provisioner) SetActivity(a *events.Activity) {
	p.activity = a
}

// diskSizes calculates boot and data disk sizes based on host capacity.
// Each VM needs ~3 GB rootfs (CoW) and 5 GB data volume by default.
func diskSizes(memoryMB int) (bootGB, dataGB int64) {
	maxVMs := memoryMB / 2048
	if maxVMs < 1 {
		maxVMs = 1
	}

	// Boot disk: OS overhead + rootfs per VM (CoW, but allocate full size as headroom).
	// Minimum 100 GB because the GCE snapshot image is ~100 GB and GCP
	// rejects disks smaller than the source image.
	bootGB = int64(10 + maxVMs*3)
	// Round up to nearest 10
	bootGB = ((bootGB + 9) / 10) * 10
	if bootGB < 100 {
		bootGB = 100
	}

	// Data disk: per-VM data volume + headroom
	dataGB = int64(maxVMs*5 + 5)
	// Round up to nearest 10
	dataGB = ((dataGB + 9) / 10) * 10

	return bootGB, dataGB
}

// ProvisionHost creates a new Host VM with the Worker Agent installed.
// It creates a DB record first (so errors are visible in the UI), then sets up
// a Cloudflare Tunnel, creates a GCE instance, and waits for agent health.
// physicalCPUs is the raw CPU thread count before oversubscription; vcpus is
// physicalCPUs * oversubscription ratio.
func (p *Provisioner) ProvisionHost(ctx context.Context, machineType string, physicalCPUs, vcpus, memoryMB int, zone string) (*store.Host, error) {
	// Use provided zone or fall back to provisioner default
	if zone == "" {
		zone = p.zone
	}
	// Derive region from zone
	region := zone
	if idx := len(zone) - 2; idx > 0 && zone[idx] == '-' {
		region = zone[:idx]
	}

	slog.Info("host.provision.started", "machine_type", machineType, "vcpus", vcpus, "memory_mb", memoryMB, "zone", zone, "region", region)

	if p.tunnel == nil {
		return nil, fmt.Errorf("tunnel manager not configured — cannot provision host without Cloudflare Tunnel")
	}

	// Ensure the firewall rule that lets the control plane reach the agent
	// control/proxy ports exists (idempotent, once per process).
	p.ensureAgentFirewall(ctx)

	vmName := fmt.Sprintf("ocm-host-%d", time.Now().UnixMilli())

	// Store the raw physical CPU count in capabilities so that
	// reconciliation can recalculate capacity_vcpus when the
	// oversubscription ratio changes.
	caps, _ := json.Marshal(map[string]int{
		"cpu_threads": physicalCPUs,
		"memory_mb":   memoryMB,
	})

	// Calculate disk capacity from boot + data disk sizes
	bootGB, dataGB := diskSizes(memoryMB)
	capacityDiskMB := int(bootGB+dataGB) * 1024

	// Create host record early so the UI can track progress and see errors
	host := &store.Host{
		VMName:           vmName,
		Zone:             zone,
		Region:           region,
		MachineType:      machineType,
		Status:           "provisioning",
		SourceImage:      strPtrOrNil(p.snapshot),
		CapacityVCPUs:    vcpus,
		CapacityMemoryMB: memoryMB,
		CapacityDiskMB:   capacityDiskMB,
		Capabilities:     caps,
	}
	if err := p.store.CreateHost(ctx, host); err != nil {
		return nil, fmt.Errorf("create host record: %w", err)
	}

	// Helper to mark the host as error with a user-visible message
	failHost := func(err error) (*store.Host, error) {
		errMsg := err.Error()
		if len(errMsg) > 500 {
			errMsg = errMsg[:500]
		}
		_ = p.store.UpdateHostStatusMessage(ctx, host.ID, "error", errMsg)
		return host, err
	}

	// --- Create Cloudflare Tunnel (before GCE so we can pass tunnel-token in metadata) ---
	var tunnelToken string
	var tunnelHostname string
	if p.tunnel != nil {
		if p.dataPlaneDomain == "" {
			return failHost(fmt.Errorf("DATA_PLANE_DOMAIN is required for tunnel provisioning"))
		}
		tunnelHostname = vmName + "." + p.dataPlaneDomain
		slog.Info("host.provision.tunnel.creating", "hostname", tunnelHostname)

		tunnelID, token, err := p.tunnel.CreateTunnel(ctx, vmName)
		if err != nil {
			return failHost(fmt.Errorf("create tunnel: %w", err))
		}
		tunnelToken = token

		if err := p.tunnel.ConfigureTunnel(ctx, tunnelID, tunnelHostname); err != nil {
			_ = p.tunnel.DeleteTunnel(ctx, tunnelID)
			return failHost(fmt.Errorf("configure tunnel: %w", err))
		}

		if err := p.tunnel.CreateDNSRoute(ctx, tunnelID, tunnelHostname); err != nil {
			slog.Error("host.provision.dns.route.failed", "hostname", tunnelHostname, "error", err)
			_ = p.tunnel.DeleteTunnel(ctx, tunnelID)
			return failHost(fmt.Errorf("create DNS route for %s: %w", tunnelHostname, err))
		}

		// Update host record with tunnel URL
		host.TunnelURL = strPtrOrNil(tunnelHostname)
	}

	// --- Create GCE instance ---
	client, err := compute.NewInstancesRESTClient(ctx)
	if err != nil {
		return failHost(fmt.Errorf("create compute client: %w", err))
	}
	defer func() { _ = client.Close() }()

	// Build metadata items
	hostIDStr := fmt.Sprintf("%d", host.ID)
	metadataItems := []*computepb.Items{
		{Key: strPtr("agent-token"), Value: &p.agentToken},
		{Key: strPtr("backend-url"), Value: &p.backendURL},
		{Key: strPtr("host-id"), Value: &hostIDStr},
	}
	if tunnelToken != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("tunnel-token"), Value: strPtr(tunnelToken),
		})
	}
	if p.rootfsGCSManifest != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("rootfs-gcs-manifest"), Value: strPtr(p.rootfsGCSManifest),
		})
	}
	if p.agentGCSManifest != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("agent-gcs-manifest"), Value: strPtr(p.agentGCSManifest),
		})
	}
	if p.browserRootfsGCSManifest != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("browser-rootfs-gcs-manifest"), Value: strPtr(p.browserRootfsGCSManifest),
		})
	}
	if p.browserRootfsVersion != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("browser-rootfs-version"), Value: strPtr(p.browserRootfsVersion),
		})
	}
	slog.Info("host.provision.browser_rootfs_manifest",
		"manifest_uri", p.browserRootfsGCSManifest,
		"version", p.browserRootfsVersion,
		"enabled", p.browserRootfsGCSManifest != "")

	// Image + bootstrap selection. With a prebaked snapshot, boot from it
	// directly (fast path, no startup-script). Otherwise boot a stock image and
	// inject a startup-script that installs the Firecracker host deps + agent on
	// first boot — so provisioning works for any operator without a golden image.
	sourceImage := fmt.Sprintf("projects/%s/global/images/%s", p.project, p.snapshot)
	if p.snapshot == "" {
		sourceImage = p.hostImage
		if sourceImage == "" {
			sourceImage = defaultHostImage
		}
		if p.artifactBucket != "" {
			metadataItems = append(metadataItems,
				&computepb.Items{Key: strPtr("artifact-bucket"), Value: strPtr(p.artifactBucket)},
				&computepb.Items{Key: strPtr("kernel-gcs-path"), Value: strPtr(p.artifactBucket + "/vmlinux")},
			)
		}
		if p.artifactBaseURL != "" {
			metadataItems = append(metadataItems, &computepb.Items{
				Key: strPtr("artifact-base-url"), Value: strPtr(p.artifactBaseURL),
			})
		}
		metadataItems = append(metadataItems, &computepb.Items{
			Key: strPtr("startup-script"), Value: strPtr(hostStartupScript),
		})
		slog.Info("host.provision.bootstrap", "vm_name", vmName, "image", sourceImage, "mode", "startup-script")
	}

	// Calculate disk sizes based on host capacity
	bootDiskGB, dataDiskGB := diskSizes(memoryMB)
	if p.dataDiskSizeGB > 0 {
		dataDiskGB = p.dataDiskSizeGB // explicit override
	}
	slog.Info("host.provision.disk_sizes", "boot_gb", bootDiskGB, "data_gb", dataDiskGB, "memory_mb", memoryMB)

	enableNestedVirt := true

	// Spot instances cost less and are reclaimable; on preemption GCE deletes
	// the VM (the host reconciler then cleans up the stale record).
	scheduling := &computepb.Scheduling{
		OnHostMaintenance: strPtr("TERMINATE"),
	}
	if strings.EqualFold(p.provisioningModel, "SPOT") {
		scheduling.ProvisioningModel = strPtr("SPOT")
		scheduling.InstanceTerminationAction = strPtr("DELETE")
		scheduling.AutomaticRestart = boolPtr(false)
		slog.Info("host.provision.spot", "vm_name", vmName)
	}

	instance := &computepb.Instance{
		Name:        &vmName,
		MachineType: strPtr(fmt.Sprintf("zones/%s/machineTypes/%s", zone, machineType)),
		Disks: []*computepb.AttachedDisk{
			{
				AutoDelete: boolPtr(true),
				Boot:       boolPtr(true),
				InitializeParams: &computepb.AttachedDiskInitializeParams{
					SourceImage: strPtr(sourceImage),
					DiskSizeGb:  int64Ptr(bootDiskGB),
					DiskType:    strPtr(fmt.Sprintf("zones/%s/diskTypes/pd-ssd", zone)),
				},
			},
			{
				AutoDelete: boolPtr(false),
				Boot:       boolPtr(false),
				DeviceName: strPtr("ocm-data"),
				InitializeParams: &computepb.AttachedDiskInitializeParams{
					DiskName:   strPtr(vmName + "-data"),
					DiskSizeGb: int64Ptr(dataDiskGB),
					DiskType:   strPtr(fmt.Sprintf("zones/%s/diskTypes/pd-balanced", zone)),
				},
			},
		},
		NetworkInterfaces: []*computepb.NetworkInterface{
			{
				AccessConfigs: []*computepb.AccessConfig{
					{
						Name: strPtr("External NAT"),
						Type: strPtr("ONE_TO_ONE_NAT"),
					},
				},
			},
		},
		ServiceAccounts: []*computepb.ServiceAccount{
			{
				Email: strPtr("default"),
				Scopes: []string{
					"https://www.googleapis.com/auth/devstorage.read_only",
					"https://www.googleapis.com/auth/logging.write",
					"https://www.googleapis.com/auth/monitoring.write",
				},
			},
		},
		Metadata: &computepb.Metadata{
			Items: metadataItems,
		},
		Tags: &computepb.Tags{
			Items: []string{"ocm-agent"},
		},
		Labels: map[string]string{
			"ocm": "true",
		},
		Scheduling: scheduling,
		AdvancedMachineFeatures: &computepb.AdvancedMachineFeatures{
			EnableNestedVirtualization: &enableNestedVirt,
		},
	}

	op, err := client.Insert(ctx, &computepb.InsertInstanceRequest{
		Project:          p.project,
		Zone:             zone,
		InstanceResource: instance,
	})
	if err != nil {
		if p.tunnel != nil && tunnelHostname != "" {
			p.cleanupTunnel(ctx, vmName, tunnelHostname)
		}
		return failHost(fmt.Errorf("insert instance: %w", err))
	}

	if err := op.Wait(ctx); err != nil {
		if p.tunnel != nil && tunnelHostname != "" {
			p.cleanupTunnel(ctx, vmName, tunnelHostname)
		}
		return failHost(fmt.Errorf("wait for instance creation: %w", err))
	}

	// Get instance details (IP addresses)
	inst, err := client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  p.project,
		Zone:     zone,
		Instance: vmName,
	})
	if err != nil {
		return failHost(fmt.Errorf("get instance: %w", err))
	}

	var externalIP, internalIP string
	if len(inst.NetworkInterfaces) > 0 {
		ni := inst.NetworkInterfaces[0]
		if ni.NetworkIP != nil {
			internalIP = *ni.NetworkIP
		}
		if len(ni.AccessConfigs) > 0 && ni.AccessConfigs[0].NatIP != nil {
			externalIP = *ni.AccessConfigs[0].NatIP
		}
	}

	vmID := ""
	if inst.Id != nil {
		vmID = fmt.Sprintf("%d", *inst.Id)
	}

	// Update host record with instance details
	host.VMID = &vmID
	host.ExternalIP = strPtrOrNil(externalIP)
	host.InternalIP = strPtrOrNil(internalIP)
	host.TunnelURL = strPtrOrNil(tunnelHostname)

	if err := p.store.UpdateHostDetails(ctx, host); err != nil {
		return failHost(fmt.Errorf("update host details: %w", err))
	}

	// Wait for agent health (poll). Stock-image bootstrap (no prebaked snapshot)
	// installs deps + downloads the agent on first boot, so it needs longer.
	healthTimeout := 5 * time.Minute
	if p.snapshot == "" {
		healthTimeout = 12 * time.Minute
	}
	slog.Info("host.provision.agent.health.waiting", "host_name", vmName, "host_id", host.ID, "timeout", healthTimeout.String())
	health, err := p.waitForAgentHealth(ctx, host, healthTimeout)
	if err != nil {
		slog.Error("host.provision.agent.health.failed", "host_name", vmName, "host_id", host.ID, "error", err)
		return failHost(fmt.Errorf("agent health check: %w", err))
	}

	// Store agent version info
	if health != nil && health.Version != "" {
		if err := p.store.UpdateHostVersions(ctx, host.ID, health.Version, version.Version); err != nil {
			slog.Warn("host.provision.version.update.failed", "host_id", host.ID, "error", err)
		}
	}

	// Set initial heartbeat so the host passes the freshness filter immediately
	if host.ExternalIP != nil {
		rootfsVer := ""
		browserRootfsVer := ""
		agentVer := ""
		if health != nil {
			agentVer = health.Version
		}
		if _, err := p.store.UpdateHostHeartbeat(ctx, host.ID, *host.ExternalIP, agentVer, rootfsVer, browserRootfsVer, "", nil); err != nil {
			slog.Warn("host.provision.initial_heartbeat.failed", "host_id", host.ID, "error", err)
		}
	}

	// Mark host as ready
	if err := p.store.UpdateHostStatus(ctx, host.ID, "ready"); err != nil {
		return host, fmt.Errorf("update host status: %w", err)
	}

	p.logHostEvent(ctx, host.ID, "host.provisioned", map[string]string{
		"vm_name": vmName, "zone": zone, "machine_type": machineType,
		"external_ip": externalIP, "tunnel_hostname": tunnelHostname,
	})

	slog.Info("host.provision.ready", "host_name", vmName, "host_id", host.ID, "internal_ip", internalIP, "hostname", tunnelHostname)
	return host, nil
}

// DrainHost marks a Host as draining so no new machines are scheduled to it.
func (p *Provisioner) DrainHost(ctx context.Context, hostID int) error {
	slog.Info("host.drain.started", "host_id", hostID)

	if err := p.store.UpdateHostStatus(ctx, hostID, "draining"); err != nil {
		return fmt.Errorf("update host status: %w", err)
	}

	p.logHostEvent(ctx, hostID, "status.changed", map[string]string{"new_status": "draining"})
	return nil
}

// DestroyHost deletes a Host VM, cleans up its Cloudflare Tunnel + DNS, and updates the DB.
func (p *Provisioner) DestroyHost(ctx context.Context, hostID int) error {
	slog.Info("host.destroy.started", "host_id", hostID)

	host, err := p.store.GetHost(ctx, hostID)
	if err != nil {
		return fmt.Errorf("get host: %w", err)
	}

	// Clean up Cloudflare Tunnel + DNS before deleting the GCE instance
	if p.tunnel != nil && host.TunnelURL != nil && *host.TunnelURL != "" {
		p.cleanupTunnel(ctx, host.VMName, *host.TunnelURL)
	}

	// Delete GCE instance
	client, err := compute.NewInstancesRESTClient(ctx)
	if err != nil {
		return fmt.Errorf("create compute client: %w", err)
	}
	defer func() { _ = client.Close() }()

	op, err := client.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  p.project,
		Zone:     host.Zone,
		Instance: host.VMName,
	})
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}

	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("wait for instance deletion: %w", err)
	}

	// Delete orphaned data disk (AutoDelete=false)
	diskClient, err := compute.NewDisksRESTClient(ctx)
	if err != nil {
		slog.Warn("host.destroy.disk_client_failed", "error", err)
	} else {
		defer func() { _ = diskClient.Close() }()
		diskName := host.VMName + "-data"
		diskOp, err := diskClient.Delete(ctx, &computepb.DeleteDiskRequest{
			Project: p.project,
			Zone:    host.Zone,
			Disk:    diskName,
		})
		if err != nil {
			slog.Warn("host.destroy.data_disk_delete_failed", "disk", diskName, "error", err)
		} else {
			if err := diskOp.Wait(ctx); err != nil {
				slog.Warn("host.destroy.data_disk_wait_failed", "disk", diskName, "error", err)
			} else {
				slog.Info("host.destroy.data_disk_deleted", "disk", diskName)
			}
		}
	}

	// Update host status
	if err := p.store.UpdateHostStatus(ctx, hostID, "stopped"); err != nil {
		return fmt.Errorf("update host status: %w", err)
	}

	p.logHostEvent(ctx, hostID, "host.destroyed", map[string]string{"vm_name": host.VMName})

	slog.Info("host.destroy.completed", "host_id", hostID, "host_name", host.VMName)
	return nil
}

// cleanupTunnel deletes the DNS record and Cloudflare Tunnel for a host.
// Errors are logged but not returned — cleanup is best-effort.
func (p *Provisioner) cleanupTunnel(ctx context.Context, vmName, tunnelHostname string) {
	slog.Info("host.tunnel.cleanup.started", "hostname", tunnelHostname)

	if err := p.tunnel.DeleteDNSRoute(ctx, tunnelHostname); err != nil {
		slog.Warn("host.tunnel.dns.delete.failed", "hostname", tunnelHostname, "error", err)
	}

	tunnelID, err := p.tunnel.FindTunnelIDByName(ctx, vmName)
	if err != nil {
		slog.Warn("host.tunnel.find.failed", "host_name", vmName, "error", err)
		return
	}

	if err := p.tunnel.DeleteTunnel(ctx, tunnelID); err != nil {
		slog.Warn("host.tunnel.delete.failed", "tunnel_id", tunnelID, "error", err)
	}
}

// waitForAgentHealth polls the agent's /health endpoint until it responds.
// ensureAgentFirewall creates an idempotent INGRESS firewall rule allowing the
// control plane to reach the agent control (:9090) and proxy (:9091) ports on
// hosts tagged "ocm-agent" (the tag the provisioner applies). Runs once per
// process. Best-effort: if it fails (e.g. the operator manages firewalls
// out-of-band or lacks compute.firewalls.create), provisioning continues, but
// the agent health check will fail unless those ports are reachable.
func (p *Provisioner) ensureAgentFirewall(ctx context.Context) {
	if p.firewallEnsured || p.project == "" {
		return
	}
	p.firewallEnsured = true // attempt once regardless of outcome

	client, err := compute.NewFirewallsRESTClient(ctx)
	if err != nil {
		slog.Warn("host.firewall.client_failed", "error", err)
		return
	}
	defer func() { _ = client.Close() }()

	const name = "ocm-agent"
	if _, err := client.Get(ctx, &computepb.GetFirewallRequest{Project: p.project, Firewall: name}); err == nil {
		return // already exists
	} else {
		var gerr *googleapi.Error
		if !errors.As(err, &gerr) || gerr.Code != 404 {
			slog.Warn("host.firewall.get_failed", "error", err)
		}
	}

	rule := &computepb.Firewall{
		Name:         strPtr(name),
		Network:      strPtr("global/networks/default"),
		Direction:    strPtr("INGRESS"),
		TargetTags:   []string{"ocm-agent"},
		SourceRanges: []string{"0.0.0.0/0"}, // agent ports are token-gated (CONTROL_ALLOWED_CIDRS + agent token)
		Allowed: []*computepb.Allowed{{
			IPProtocol: strPtr("tcp"),
			Ports:      []string{"9090", "9091"},
		}},
		Description: strPtr("OCM agent control(:9090)/proxy(:9091) — matches provisioner tag ocm-agent"),
	}
	op, err := client.Insert(ctx, &computepb.InsertFirewallRequest{Project: p.project, FirewallResource: rule})
	if err != nil {
		slog.Warn("host.firewall.create_failed", "error", err)
		return
	}
	if err := op.Wait(ctx); err != nil {
		slog.Warn("host.firewall.create_wait_failed", "error", err)
		return
	}
	slog.Info("host.firewall.created", "name", name, "ports", "9090,9091", "target_tag", "ocm-agent")
}

func (p *Provisioner) waitForAgentHealth(ctx context.Context, host *store.Host, timeout time.Duration) (*agentclient.HealthResponse, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		health, err := p.agentClient.Health(ctx, host)
		if err == nil && health.Status == "ok" {
			return health, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			// retry
		}
	}

	return nil, fmt.Errorf("timeout waiting for agent health after %s", timeout)
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func int64Ptr(i int64) *int64 { return &i }

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// logHostEvent emits a structured log and records the event via the activity logger.
func (p *Provisioner) logHostEvent(ctx context.Context, hostID int, eventType string, meta map[string]string) {
	slog.Info("host_event", "host_id", hostID, "event_type", eventType, "metadata", meta)
	if p.activity != nil {
		detail := make(map[string]any, len(meta))
		for k, v := range meta {
			detail[k] = v
		}
		p.activity.Log(ctx, events.LogParams{
			Category:  "host",
			Action:    eventType,
			Status:    "success",
			ActorType: "system",
			HostID:    &hostID,
			Summary:   fmt.Sprintf("Host %s: %s", eventType, meta["vm_name"]),
			Detail:    detail,
		})
	}
}
