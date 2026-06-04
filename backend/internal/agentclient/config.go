package agentclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

const repairOpenClawConfigSymlinkScript = `
set -eu
state="${OPENCLAW_STATE_DIR:-/home/openclaw/.openclaw}"
stable="$state/openclaw.json"
current="$state/config-current"
target="$current/openclaw.json"

if [ -e "$stable" ] && [ ! -L "$stable" ] && [ -d "$current" ]; then
	orphan="$state/openclaw.json.orphan.$(date -u +%Y%m%dT%H%M%SZ)"
	cp -p "$stable" "$target"
	mv "$stable" "$orphan"
	ln -s "config-current/openclaw.json" "$stable"
	chown openclaw:openclaw "$target" 2>/dev/null || true
	chown -h openclaw:openclaw "$stable" 2>/dev/null || true
	chmod 600 "$target" 2>/dev/null || true
	echo "repaired openclaw.json symlink; orphan=$orphan"
elif [ ! -e "$stable" ] && [ -d "$current" ] && [ -f "$target" ]; then
	ln -s "config-current/openclaw.json" "$stable"
	chown -h openclaw:openclaw "$stable" 2>/dev/null || true
	echo "restored openclaw.json symlink"
fi
`

// ConfigUnset calls `openclaw config unset <path>` on a running machine
// via the agent exec endpoint. This removes the key entirely from openclaw.json.
func (c *Client) ConfigUnset(ctx context.Context, host *store.Host, machineID, proxyToken, path string) error {
	cmd := []string{"openclaw", "config", "unset", path}
	result, err := c.ExecCommand(ctx, host, machineID, proxyToken, cmd)
	if err != nil {
		return fmt.Errorf("config unset %s: %w", path, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("config unset %s exit %d: %s", path, result.ExitCode, result.Stderr)
	}
	return nil
}

func (c *Client) repairOpenClawConfigSymlink(ctx context.Context, host *store.Host, machineID, proxyToken string) error {
	result, err := c.ExecCommand(ctx, host, machineID, proxyToken, []string{"sh", "-c", repairOpenClawConfigSymlinkScript})
	if err != nil {
		return fmt.Errorf("repair openclaw config symlink: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("repair openclaw config symlink exit %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}

// ConfigOp represents a single config set or unset operation.
type ConfigOp struct {
	Op         string // "set" or "unset"
	Path       string
	Value      string // only for "set"
	StrictJSON bool   // only for "set"
	Replace    bool   // only for "set"; pass --replace for owned object values
}

// batchEntry is the JSON structure for openclaw config set --batch-json.
type batchEntry struct {
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// ConfigBatch executes a set of config operations on a running machine.
// All "set" ops are bundled into a single `openclaw config set --batch-json`
// exec call. "unset" ops are executed individually (batch mode only supports set).
// Returns a slice of errors (empty if all succeeded).
func (c *Client) ConfigBatch(ctx context.Context, host *store.Host, machineID, proxyToken string, ops []ConfigOp) []error {
	if len(ops) == 0 {
		return nil
	}

	var setOps []ConfigOp
	var replaceSetOps []ConfigOp
	var unsetOps []ConfigOp
	for _, op := range ops {
		switch op.Op {
		case "set":
			if op.Replace {
				replaceSetOps = append(replaceSetOps, op)
			} else {
				setOps = append(setOps, op)
			}
		case "unset":
			unsetOps = append(unsetOps, op)
		default:
			return []error{fmt.Errorf("unknown config op: %s", op.Op)}
		}
	}

	var errs []error
	modified := false

	runSetBatch := func(batchOps []ConfigOp, replace bool) {
		if len(batchOps) == 0 {
			return
		}
		entries := make([]batchEntry, 0, len(batchOps))
		for _, op := range batchOps {
			entry := batchEntry{Path: op.Path}
			if op.StrictJSON {
				// Unmarshal so it serializes as a proper JSON value, not an escaped string
				var parsed interface{}
				if err := json.Unmarshal([]byte(op.Value), &parsed); err != nil {
					errs = append(errs, fmt.Errorf("config set %s: invalid JSON value: %w", op.Path, err))
					continue
				}
				entry.Value = parsed
			} else {
				entry.Value = op.Value
			}
			entries = append(entries, entry)
		}

		if len(entries) > 0 {
			batchJSON, err := json.Marshal(entries)
			if err != nil {
				errs = append(errs, fmt.Errorf("marshal batch json: %w", err))
				return
			}
			cmd := []string{"openclaw", "config", "set"}
			if replace {
				cmd = append(cmd, "--replace")
			}
			cmd = append(cmd, "--batch-json", string(batchJSON))
			result, err := c.ExecCommand(ctx, host, machineID, proxyToken, cmd)
			if err != nil {
				errs = append(errs, fmt.Errorf("config set --batch-json: %w", err))
			} else if result.ExitCode != 0 {
				errs = append(errs, fmt.Errorf("config set --batch-json exit %d: %s", result.ExitCode, result.Stderr))
			} else {
				modified = true
			}
		}
	}

	// Replace batches first so dependent values, such as model.primary, can be
	// set against the new owned catalog instead of stale VM-local entries.
	runSetBatch(replaceSetOps, true)
	runSetBatch(setOps, false)

	// Unset ops are individual calls (no batch unset in openclaw CLI)
	for _, op := range unsetOps {
		if err := c.ConfigUnset(ctx, host, machineID, proxyToken, op.Path); err != nil {
			errs = append(errs, err)
		} else {
			modified = true
		}
	}

	if modified {
		if err := c.repairOpenClawConfigSymlink(ctx, host, machineID, proxyToken); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// HermesConfigSync asks a Hermes VM to fetch the latest desired config from
// metadata and merge OCM-managed keys into its persisted Hermes home.
func (c *Client) HermesConfigSync(ctx context.Context, host *store.Host, machineID, proxyToken string, restartGateway bool) error {
	cmd := []string{"ocm-hermes-config", "sync"}
	if restartGateway {
		cmd = append(cmd, "--restart-gateway")
	}
	result, err := c.ExecCommand(ctx, host, machineID, proxyToken, cmd)
	if err != nil {
		return fmt.Errorf("hermes config sync: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("hermes config sync exit %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}
