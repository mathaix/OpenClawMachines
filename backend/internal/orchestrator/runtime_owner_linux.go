//go:build linux

package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"
	"unsafe"

	"github.com/coreos/go-systemd/v22/dbus"
	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	godbus "github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"
)

const (
	runtimeOwnerKindDirect     = "direct"
	runtimeOwnerKindSystemdVML = "systemd-unit"
)

type firecrackerRuntimeOwner interface {
	kind() string
	ownerRef(vmID string, browser bool) string
	start(ctx context.Context, vmID string, browser bool, socketPath string, fcCfg firecracker.Config, stdout, stderr io.Writer) (*firecracker.Machine, int, context.CancelFunc, error)
	stop(ctx context.Context, ownerRef string, machine *firecracker.Machine, cancel context.CancelFunc, graceTimeout time.Duration) error
}

type directFirecrackerRuntimeOwner struct{}
type systemdUnitRuntimeOwner struct{}

type systemdUnitState struct {
	Unit        string
	LoadState   string
	ActiveState string
	SubState    string
	MainPID     int
}

var firecrackerBinaryPath = resolveFirecrackerBinaryPath()

func (d directFirecrackerRuntimeOwner) kind() string {
	return runtimeOwnerKindDirect
}

func (d directFirecrackerRuntimeOwner) ownerRef(_ string, _ bool) string {
	return ""
}

func firecrackerConfigWithVMID(fcCfg firecracker.Config, vmID string) firecracker.Config {
	if strings.TrimSpace(vmID) != "" {
		fcCfg.VMID = vmID
	}
	return fcCfg
}

func directFirecrackerCommand(ctx context.Context, socketPath, vmID string, stdout, stderr io.Writer) *exec.Cmd {
	builder := firecracker.VMCommandBuilder{}.
		WithSocketPath(socketPath).
		WithBin("firecracker")
	if strings.TrimSpace(vmID) != "" {
		builder = builder.AddArgs("--id", vmID)
	}
	return builder.
		WithStdout(stdout).
		WithStderr(stderr).
		Build(ctx)
}

func (d directFirecrackerRuntimeOwner) start(ctx context.Context, vmID string, browser bool, socketPath string, fcCfg firecracker.Config, stdout, stderr io.Writer) (*firecracker.Machine, int, context.CancelFunc, error) {
	_ = browser
	fcCfg = firecrackerConfigWithVMID(fcCfg, vmID)
	vmCtx, vmCancel := context.WithCancel(context.Background())
	machineOpts := []firecracker.Opt{
		firecracker.WithProcessRunner(directFirecrackerCommand(vmCtx, socketPath, fcCfg.VMID, stdout, stderr)),
	}

	machine, err := firecracker.NewMachine(vmCtx, fcCfg, machineOpts...)
	if err != nil {
		vmCancel()
		return nil, 0, nil, fmt.Errorf("create firecracker machine: %w", err)
	}
	if err := machine.Start(vmCtx); err != nil {
		vmCancel()
		return nil, 0, nil, fmt.Errorf("start firecracker: %w", err)
	}
	pid, err := machine.PID()
	if err != nil {
		vmCancel()
		_ = machine.StopVMM()
		return nil, 0, nil, fmt.Errorf("read firecracker pid: %w", err)
	}
	return machine, pid, vmCancel, nil
}

func (d directFirecrackerRuntimeOwner) stop(ctx context.Context, _ string, machine *firecracker.Machine, cancel context.CancelFunc, graceTimeout time.Duration) error {
	var stopErr error
	if machine != nil {
		if graceTimeout <= 0 {
			graceTimeout = 10 * time.Second
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, graceTimeout)
		defer shutdownCancel()

		if err := machine.Shutdown(shutdownCtx); err != nil {
			stopErr = err
			_ = machine.StopVMM()
		}
	}
	if cancel != nil {
		cancel()
	}
	return stopErr
}

func defaultRuntimeOwnerKind(kind string) string {
	if kind == "" {
		return runtimeOwnerKindDirect
	}
	return kind
}

func runtimeOwnerFromKind(kind string) (firecrackerRuntimeOwner, error) {
	switch defaultRuntimeOwnerKind(kind) {
	case runtimeOwnerKindDirect:
		return directFirecrackerRuntimeOwner{}, nil
	case runtimeOwnerKindSystemdVML:
		return systemdUnitRuntimeOwner{}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime owner kind %q", kind)
	}
}

func machineSystemdUnitName(machineID string) string {
	return "ocm-vm-" + machineID + ".service"
}

func browserSystemdUnitName(browserVMID string) string {
	return "ocm-browser-vm-" + browserVMID + ".service"
}

func (s systemdUnitRuntimeOwner) kind() string {
	return runtimeOwnerKindSystemdVML
}

func (s systemdUnitRuntimeOwner) ownerRef(vmID string, browser bool) string {
	if browser {
		return browserSystemdUnitName(vmID)
	}
	return machineSystemdUnitName(vmID)
}

func (s systemdUnitRuntimeOwner) start(ctx context.Context, vmID string, browser bool, socketPath string, fcCfg firecracker.Config, stdout, stderr io.Writer) (*firecracker.Machine, int, context.CancelFunc, error) {
	_ = stdout
	_ = stderr
	fcCfg = firecrackerConfigWithVMID(fcCfg, vmID)
	startCtx := ctx
	if startCtx == nil {
		startCtx = context.Background()
	}
	if err := systemdAvailable(ctx); err != nil {
		return nil, 0, nil, err
	}
	if firecrackerBinaryPath == "" {
		return nil, 0, nil, fmt.Errorf("systemd runtime owner requires firecracker in PATH")
	}

	unit := s.ownerRef(vmID, browser)
	vmCtx, vmCancel := context.WithCancel(context.Background())
	machine, err := firecracker.NewMachine(vmCtx, fcCfg)
	if err != nil {
		vmCancel()
		return nil, 0, nil, fmt.Errorf("create firecracker machine: %w", err)
	}
	machine.Handlers.FcInit = machine.Handlers.FcInit.Swap(firecracker.Handler{
		Name: firecracker.StartVMMHandlerName,
		Fn: func(handlerCtx context.Context, m *firecracker.Machine) error {
			return startFirecrackerUnit(handlerCtx, m, unit, socketPath, fcCfg.VMID)
		},
	})
	// Keep the machine's lifetime detached from the request after boot, but
	// make the boot handshake itself respect the caller's deadline.
	if err := machine.Start(startCtx); err != nil {
		vmCancel()
		return nil, 0, nil, fmt.Errorf("start firecracker via systemd-unit: %w", err)
	}

	state, err := systemdUnitStateReader(unit)
	if err != nil {
		vmCancel()
		_ = systemdUnitStopper(unit)
		return nil, 0, nil, err
	}
	if state.MainPID <= 0 {
		vmCancel()
		_ = systemdUnitStopper(unit)
		return nil, 0, nil, fmt.Errorf("invalid systemd MainPID for %s: %d", unit, state.MainPID)
	}

	return machine, state.MainPID, vmCancel, nil
}

func (s systemdUnitRuntimeOwner) stop(ctx context.Context, ownerRef string, machine *firecracker.Machine, cancel context.CancelFunc, graceTimeout time.Duration) error {
	var stopErr error
	if ownerRef == "" {
		stopErr = fmt.Errorf("missing systemd ownerRef for VM stop")
	} else {
		shutdownCtx := ctx
		var shutdownCancel context.CancelFunc
		if graceTimeout > 0 {
			shutdownCtx, shutdownCancel = context.WithTimeout(ctx, graceTimeout)
			defer shutdownCancel()
		}
		if err := systemdUnitStopperContext(shutdownCtx, ownerRef); err != nil {
			stopErr = err
		}
	}
	if cancel != nil {
		cancel()
	}
	return stopErr
}

// systemdDBusConnector opens a connection to the local systemd. Overridable
// in tests so we don't depend on a running dbus/systemd during unit tests.
var systemdDBusConnector = func(ctx context.Context) (*dbus.Conn, error) {
	return dbus.NewWithContext(ctx)
}

var systemdDBusCloser = func(conn *dbus.Conn) error {
	conn.Close()
	return nil
}

var systemdStartTransientUnit = func(ctx context.Context, conn *dbus.Conn, name string, mode string, properties []dbus.Property, ch chan<- string) (int, error) {
	return conn.StartTransientUnitContext(ctx, name, mode, properties, ch)
}

var firecrackerSocketProbe = func(socketPath string) error {
	client := firecracker.NewClient(socketPath, logrus.NewEntry(logrus.New()), false)
	_, err := client.GetMachineConfiguration()
	return err
}

func resolveFirecrackerBinaryPath() string {
	path, err := exec.LookPath("firecracker")
	if err != nil {
		return ""
	}
	return path
}

func systemdAvailable(ctx context.Context) error {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return fmt.Errorf("systemd runtime owner requires a running systemd host: %w", err)
	}
	conn, err := systemdDBusConnector(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd dbus: %w", err)
	}
	if conn != nil {
		defer func() { _ = systemdDBusCloser(conn) }()
	}
	return nil
}

func startFirecrackerUnit(ctx context.Context, machine *firecracker.Machine, unit, socketPath, vmID string) error {
	conn, err := systemdDBusConnector(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd dbus: %w", err)
	}
	if conn != nil {
		defer func() { _ = systemdDBusCloser(conn) }()
	}

	// Register the unit-stop cleanup BEFORE StartTransientUnit. The
	// firecracker-go-sdk's machine.Start defers doCleanup() on any handler
	// error, so any failure after this point — including a ctx cancel between
	// a successful StartTransientUnit and the end of waitForSystemdUnitActive
	// — will now tear the unit back down. Registering after the wait was a
	// stranding bug: a mid-boot cancel left an active ocm-vm-*.service + live
	// Firecracker with no in-memory record and no cleanup hook.
	//
	// systemdUnitStopper treats a missing unit as success, so running this
	// cleanup before the unit actually exists (e.g. StartTransientUnit itself
	// fails) is a no-op.
	appendMachineCleanupFunc(machine, func() error {
		stopErr := systemdUnitStopper(unit)
		removeErr := os.Remove(socketPath)
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		if stopErr != nil {
			return stopErr
		}
		return removeErr
	})

	resultCh := make(chan string, 1)
	if _, err := systemdStartTransientUnit(ctx, conn, unit, "replace", systemdTransientUnitProperties(socketPath, vmID), resultCh); err != nil {
		return fmt.Errorf("start transient unit %s: %w", unit, err)
	}

	select {
	case result := <-resultCh:
		if result != "done" && result != "skipped" {
			return fmt.Errorf("start transient unit %s: job result %q", unit, result)
		}
	case <-ctx.Done():
		return fmt.Errorf("start transient unit %s: %w", unit, ctx.Err())
	}

	if _, err := waitForSystemdUnitActive(ctx, unit); err != nil {
		return err
	}

	if err := waitForFirecrackerSocket(ctx, socketPath); err != nil {
		return fmt.Errorf("firecracker socket %s not ready: %w", socketPath, err)
	}
	return nil
}

func systemdTransientUnitProperties(socketPath, vmID string) []dbus.Property {
	return []dbus.Property{
		dbus.PropExecStart([]string{firecrackerBinaryPath, "--api-sock", socketPath, "--id", vmID, "--no-seccomp"}, false),
		dbus.PropType("exec"),
		dbus.PropDescription("OCM Firecracker VM " + vmID),
		{Name: "Restart", Value: godbus.MakeVariant("no")},
		{Name: "KillMode", Value: godbus.MakeVariant("process")},
		{Name: "CollectMode", Value: godbus.MakeVariant("inactive-or-failed")},
	}
}

// waitForSystemdUnitActiveTransientErrorBudget is the number of consecutive
// systemdUnitStateReader failures tolerated before giving up. A brief
// dbus/systemd hiccup (the socket is momentarily unavailable, or a
// GetUnitProperties call races unit lookup) must not be fatal — otherwise
// VM startup is flaky under load. The budget is reset to zero on any
// successful read, so sustained failure still surfaces.
const waitForSystemdUnitActiveTransientErrorBudget = 5

func waitForSystemdUnitActive(ctx context.Context, unit string) (int, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	transientErrors := 0
	var lastErr error

	for {
		state, err := systemdUnitStateReader(unit)
		if err != nil {
			// Terminal errors (unit genuinely missing) map to LoadState
			// not-found elsewhere; this branch only fires on transient
			// read failures. Give dbus a few retries before giving up.
			transientErrors++
			lastErr = err
			if transientErrors > waitForSystemdUnitActiveTransientErrorBudget {
				return 0, fmt.Errorf("read systemd unit state for %s after %d retries: %w", unit, transientErrors-1, err)
			}
			slog.Warn("waitForSystemdUnitActive.transient_read_error",
				"unit", unit,
				"retry", transientErrors,
				"budget", waitForSystemdUnitActiveTransientErrorBudget,
				"error", err)

			select {
			case <-ctx.Done():
				return 0, fmt.Errorf("wait for systemd unit %s active: %w (last transient error: %v)", unit, ctx.Err(), lastErr)
			case <-ticker.C:
			}
			continue
		}
		// Successful read — reset the transient-error budget so we can
		// tolerate another burst later in the boot if one arrives.
		transientErrors = 0

		if state.ActiveState == "active" && state.MainPID > 0 {
			return state.MainPID, nil
		}
		// Fast-fail on terminal states so callers see the real failure
		// immediately instead of burning the full boot deadline waiting
		// for a dead unit to become active. Before this, a crashed
		// ExecStart would block the entire startup timeout.
		//
		// "failed"    — ExecStart crashed / returned non-zero.
		// "not-found" — the transient unit was GC'd before we could
		//               confirm activation (e.g. CollectMode=inactive-or-failed
		//               fired early).
		if state.ActiveState == "failed" {
			return 0, fmt.Errorf("systemd unit %s entered failed state (sub=%q)", unit, state.SubState)
		}
		if state.LoadState == "not-found" {
			return 0, fmt.Errorf("systemd unit %s vanished before becoming active", unit)
		}

		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("wait for systemd unit %s active: %w", unit, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForFirecrackerSocket(ctx context.Context, socketPath string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(socketPath); err == nil {
			if err := firecrackerSocketProbe(socketPath); err == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func appendMachineCleanupFunc(machine *firecracker.Machine, fn func() error) {
	if machine == nil || fn == nil {
		return
	}
	field := reflect.ValueOf(machine).Elem().FieldByName("cleanupFuncs")
	cleanupFuncs := (*[]func() error)(unsafe.Pointer(field.UnsafeAddr()))
	*cleanupFuncs = append(*cleanupFuncs, fn)
}

// isUnitNotFoundErr reports whether a dbus error means the unit doesn't exist.
// Systemd surfaces "missing unit" through several error shapes depending on
// how we arrived there; all of them need to map to LoadState=not-found so the
// recovery state machine in resolveRecoveredPID takes the "unit_missing"
// cleanup branch instead of the "unit_state_unavailable" quarantine branch.
//
// Shapes we match on:
//   - "Unit X not loaded." / "Unit X not found." / "NoSuchUnit"
//     (systemd's Manager.StopUnit and friends, when passed a name that
//     systemd has never loaded)
//   - "org.freedesktop.DBus.Error.UnknownObject: Object does not exist at
//     path /org/freedesktop/systemd1/unit/..."
//     (the dominant path for our --collect transient units: once the unit
//     exits, systemd garbage-collects its dbus object, and any subsequent
//     property fetch returns UnknownObject rather than a systemd-level error)
func isUnitNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not loaded") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such unit") ||
		strings.Contains(msg, "nosuchunit") ||
		strings.Contains(msg, "unknown object") ||
		strings.Contains(msg, "unknownobject") ||
		strings.Contains(msg, "does not exist")
}

func unitPropString(props map[string]interface{}, key string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func unitPropMainPID(props map[string]interface{}) int {
	v, ok := props["MainPID"]
	if !ok {
		return 0
	}
	switch p := v.(type) {
	case uint32:
		return int(p)
	case int32:
		return int(p)
	case uint64:
		return int(p)
	case int64:
		return int(p)
	}
	return 0
}

var systemdUnitStateReader = func(unit string) (systemdUnitState, error) {
	ctx := context.Background()
	conn, err := systemdDBusConnector(ctx)
	if err != nil {
		return systemdUnitState{}, fmt.Errorf("connect to systemd dbus: %w", err)
	}
	defer conn.Close()

	unitProps, err := conn.GetUnitPropertiesContext(ctx, unit)
	if err != nil {
		if isUnitNotFoundErr(err) {
			return systemdUnitState{Unit: unit, LoadState: "not-found"}, nil
		}
		return systemdUnitState{}, fmt.Errorf("read systemd unit props for %s: %w", unit, err)
	}

	state := systemdUnitState{
		Unit:        unit,
		LoadState:   unitPropString(unitProps, "LoadState"),
		ActiveState: unitPropString(unitProps, "ActiveState"),
		SubState:    unitPropString(unitProps, "SubState"),
	}

	// MainPID lives on the Service interface, not the generic Unit interface.
	// Skip the second round-trip for units that aren't actually loaded.
	if state.LoadState == "not-found" || state.LoadState == "" {
		return state, nil
	}
	serviceProps, err := conn.GetUnitTypePropertiesContext(ctx, unit, "Service")
	if err != nil {
		if isUnitNotFoundErr(err) {
			return state, nil
		}
		return state, fmt.Errorf("read systemd service props for %s: %w", unit, err)
	}
	state.MainPID = unitPropMainPID(serviceProps)
	return state, nil
}

var systemdUnitStopper = func(unit string) error {
	return systemdUnitStopperContext(context.Background(), unit)
}

var systemdUnitStopperContext = func(ctx context.Context, unit string) error {
	conn, err := systemdDBusConnector(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd dbus: %w", err)
	}
	defer conn.Close()

	// resultCh buffer size of 1 is load-bearing: go-systemd's JobRemoved
	// dispatcher is serialized ("Until the write is unblocked, the Conn
	// object cannot handle other jobs") and if we return via ctx.Done()
	// below without reading, an unbuffered channel would deadlock the
	// dispatcher. With buffer=1 the dispatcher's write always succeeds and
	// we can abandon the orphaned value on ctx expiry.
	resultCh := make(chan string, 1)
	if _, err := conn.StopUnitContext(ctx, unit, "replace", resultCh); err != nil {
		// A missing unit is already in the desired state — treat as success
		// so Destroy/Stop are idempotent.
		if isUnitNotFoundErr(err) {
			return nil
		}
		return fmt.Errorf("stop systemd unit %s: %w", unit, err)
	}

	select {
	case result := <-resultCh:
		if result != "done" && result != "skipped" {
			return fmt.Errorf("stop systemd unit %s: job result %q", unit, result)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop systemd unit %s: %w", unit, ctx.Err())
	}
}

var systemdUnitLister = func(patterns ...string) ([]systemdUnitState, error) {
	ctx := context.Background()
	conn, err := systemdDBusConnector(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd dbus: %w", err)
	}
	defer conn.Close()

	units, err := conn.ListUnitsByPatternsContext(ctx, nil, patterns)
	if err != nil {
		return nil, fmt.Errorf("list systemd units %v: %w", patterns, err)
	}
	states := make([]systemdUnitState, 0, len(units))
	for _, u := range units {
		states = append(states, systemdUnitState{
			Unit:        u.Name,
			LoadState:   u.LoadState,
			ActiveState: u.ActiveState,
			SubState:    u.SubState,
		})
	}
	return states, nil
}
