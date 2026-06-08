# Host Enrollment Guide

## Overview

This guide walks you through adding a non-GCP host (OVH Dedicated, Hetzner, or your own server) to the OCM fleet.

## Prerequisites

Your server needs:

- **OS**: Ubuntu 22.04 or 24.04 LTS
- **KVM**: Hardware virtualization enabled (run `kvm-ok` to verify)
- **Network**: Public IPv4 address
- **Access**: Root or sudo access
- **Disk**: XFS-formatted scratch disk recommended for Firecracker reflink performance
- **Firewall**: Port 9090 accessible from your control-plane deployment (for agent control API)

## Step 1: Generate Enrollment Token

From the admin panel at `/admin#hosts`, click **"Enroll Host"** and fill in:

- **Provider**: `ovhcloud`, `hetzner`, or `customer_owned`
- **Expiry**: Token validity (default 24 hours)

The panel will show you an install command to copy.

**Or via API:**

```bash
curl -X POST https://api.yourdomain.com/api/admin/hosts/enrollment-tokens \
  -H "Content-Type: application/json" \
  -H "Cookie: <cf-access-cookie>" \
  -d '{"provider": "ovhcloud", "expires_in_hours": 24}'
```

## Step 2: Prepare the Server

SSH into your server and ensure KVM is available:

```bash
# Check KVM support
sudo apt-get update && sudo apt-get install -y cpu-checker
kvm-ok
# Should say: "KVM acceleration can be used"

# Create directories
sudo mkdir -p /etc/ocm-agent /var/lib/ocm/images /var/lib/ocm/vms /var/lib/ocm/data

# If you have a separate data disk, mount it as XFS at /var/lib/ocm/vms
# This enables reflink for fast VM rootfs copies
```

## Step 3: Run the Install Script

Copy the install command from the admin panel and run it on your server:

```bash
curl -sL https://api.yourdomain.com/api/agent/install | sudo bash -s -- <YOUR_TOKEN>
```

This will:
1. Register the host with the control plane
2. Create a Cloudflare Tunnel automatically (for user-facing traffic)
3. Install cloudflared and write the tunnel token to agent config
4. Write configuration to `/etc/ocm-agent/agent.env`
5. Write GCS credentials (for rootfs/agent updates) to `/etc/ocm-agent/gcs-key.json`
6. Install and start the `ocm-agent` systemd service

## Step 4: Verify Enrollment

Check agent status:

```bash
sudo systemctl status ocm-agent
sudo journalctl -u ocm-agent --since "5 min ago" | grep heartbeat
```

The host should appear in the admin panel within 60 seconds with status `ready`.

The Cloudflare tunnel is automatically created during registration. The agent starts
cloudflared using the token provided during enrollment. Per-VM tunnels for workspace
access are created when machines are started.

## Troubleshooting

### Agent won't start
```bash
sudo journalctl -u ocm-agent --since "10 min ago"
```

### Heartbeat not reaching control plane
```bash
# Test connectivity
curl -s https://api.yourdomain.com/health
```

### KVM not available
```bash
# Check kernel modules
lsmod | grep kvm
# If missing:
sudo modprobe kvm kvm_intel  # or kvm_amd
```

### Install Claude Code for debugging
```bash
# SSH to the host, then:
curl -sL https://cli.claude.ai/install.sh | bash
```

## Architecture

```
Control Plane
    ↕ HTTPS (heartbeat, agent control)
OVH Host
    ├── ocm-agent (systemd service)
    │   ├── Firecracker orchestrator
    │   ├── Metadata server (bridge gateway:80)
    │   ├── API proxy
    │   └── Self-update from GCS
    ├── cloudflared (tunnel for user traffic)
    └── MicroVMs (Firecracker)
        ├── VM 1 (user workspace)
        ├── VM 2 (user workspace)
        └── ...
```
