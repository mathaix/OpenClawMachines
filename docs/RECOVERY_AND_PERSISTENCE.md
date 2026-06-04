# Operations: Recovery, Scaling, and Persistence

This document outlines how OpenClaw Machines handles infrastructure failures, traffic spikes, and stateful user sessions.

---

## 1. Persistence Strategy (The "Stateful Brain")
To ensure that agents "remember" users and maintain logins across restarts, we follow the **Profile-on-Persistence** rule.

### Technical Implementation:
- **Persistent Volumes:** Every machine is assigned a dedicated .ext4 block device mounted at /workspace.
- **Browser Sessions:** Playwright is configured to use --user-data-dir=/workspace/browser-profile. This ensures cookies for Notion, Gmail, etc., survive VM moves.
- **Agent Memory:** The OpenClaw state directory is mapped to /workspace/openclaw, persisting the SQLite history.
- **Identity:** AI keys and Discord tokens are **never** stored on disk; they are injected into RAM on every boot via the Metadata Service.

---

## 2. Recovery Scenarios (The "Watchdog")

### Case A: Worker Agent Process Crashes
- **Impact:** Control and visual streaming stop. MicroVMs **keep running**.
- **Fix:** Systemd restarts the Agent (Restart=always).
- **Re-attach:** On boot, the Agent reads vms.json and re-adopts existing Firecracker processes via their Unix sockets.

### Case B: Entire Host VM Dies (Preemption/Power)
- **Impact:** All agents on that host go offline.
- **Fix:** Backend detects heartbeat timeout.
- **Failover:** User clicks Start -> Scheduler assigns a new Host -> Metadata service delivers keys -> Persistent Disk is attached -> Agent resumes instantly.

---

## 3. Scaling for Viral Growth
To survive a viral uptake (e.g., a high-profile tweet), the platform must grow elastically.

- **Host Over-provisioning:** We target 64-100 agents per 32-core host. During a spike, we can push this to 128 (4:1 over-provisioning) as most agents are idle.
- **Automatic Provisioning:** When total fleet capacity exceeds 80%, the Control Plane automatically calls the GCP API to spin up a new Host VM from our Golden Image.
- **Regional Warm-up:** We maintain at least 1 "ready" host in US, EU, and APAC to ensure 5-second boot times for the first wave of users.

---

## 4. Snapshotting & Migration
Moving a machine from one host to another (e.g., for maintenance or regional hopping):

1. **Pause:** Trigger Firecracker /snapshot/create (Memory + CPU state).
2. **Transfer:** Move the Disk image and Memory snapshot to the new host via GCS.
3. **Resume:** Call /snapshot/load on the new host.
4. **Latency:** Total migration time ~10-15 seconds for a 1GB machine.
