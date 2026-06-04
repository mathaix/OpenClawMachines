//go:build linux

package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	systemddb "github.com/coreos/go-systemd/v22/dbus"
	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
)

// machineCleanupFuncs reads the unexported cleanupFuncs slice from a
// firecracker.Machine. Used to verify cleanup registration order in tests.
func machineCleanupFuncs(m *firecracker.Machine) []func() error {
	field := reflect.ValueOf(m).Elem().FieldByName("cleanupFuncs")
	return *(*[]func() error)(unsafe.Pointer(field.UnsafeAddr()))
}

// TestIsUnitNotFoundErr locks in the set of error shapes that the runtime
// owner treats as "unit has already vanished from systemd." Each row
// corresponds to a shape we've actually observed from systemd/dbus during
// recovery, plus a few negatives that must NOT be silently swallowed as
// "unit gone" — those need to surface as real errors so recovery quarantines
// with unit_state_unavailable instead of unit_missing.
func TestIsUnitNotFoundErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Positive cases — each matches the intent of the commit comment.
		{"nil is not not-found", nil, false},
		{
			"systemctl-style not loaded",
			errors.New("Unit ocm-vm-foo.service not loaded."),
			true,
		},
		{
			"systemctl-style not found",
			errors.New("Unit ocm-vm-foo.service not found."),
			true,
		},
		{
			"no such unit (words)",
			errors.New("No such unit ocm-vm-foo.service"),
			true,
		},
		{
			"NoSuchUnit (dbus error class)",
			errors.New("org.freedesktop.systemd1.NoSuchUnit: Unit ocm-vm-foo.service not loaded."),
			true,
		},
		{
			"dbus UnknownObject (class name)",
			errors.New("org.freedesktop.DBus.Error.UnknownObject: Object does not exist at path /org/freedesktop/systemd1/unit/ocm_2dvm_2dfoo_2eservice"),
			true,
		},
		{
			"dbus UnknownObject (words only)",
			errors.New("unknown object at path ..."),
			true,
		},
		{
			"does not exist (body only)",
			errors.New("Object does not exist at path /some/path"),
			true,
		},
		{
			"mixed case folds correctly",
			errors.New("NOT LOADED"),
			true,
		},

		// Negative cases — must NOT match, or we'd silently swallow real errors.
		{
			"connection refused",
			errors.New("dial unix /run/systemd/private: connect: connection refused"),
			false,
		},
		{
			"permission denied",
			errors.New("dial unix /run/systemd/private: connect: permission denied"),
			false,
		},
		{
			"unrelated dbus error",
			errors.New("org.freedesktop.DBus.Error.InvalidArgs: Invalid argument"),
			false,
		},
		{
			"context deadline",
			errors.New("context deadline exceeded"),
			false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnitNotFoundErr(tc.err); got != tc.want {
				t.Errorf("isUnitNotFoundErr(%q) = %v, want %v", errorString(tc.err), got, tc.want)
			}
		})
	}
}

func errorString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestFirecrackerConfigWithVMIDUsesLogicalID(t *testing.T) {
	cfg := firecrackerConfigWithVMID(firecracker.Config{VMID: "random-sdk-id"}, "vm-test")
	if cfg.VMID != "vm-test" {
		t.Fatalf("expected logical VMID %q, got %q", "vm-test", cfg.VMID)
	}
}

func TestDirectFirecrackerCommandIncludesInstanceID(t *testing.T) {
	cmd := directFirecrackerCommand(context.Background(), "/tmp/fc.sock", "vm-test", nil, nil)
	got := cmd.Args
	want := []string{"firecracker", "--api-sock", "/tmp/fc.sock", "--id", "vm-test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct firecracker args = %#v, want %#v", got, want)
	}
}

func TestWaitForFirecrackerSocket(t *testing.T) {
	originalProbe := firecrackerSocketProbe
	t.Cleanup(func() {
		firecrackerSocketProbe = originalProbe
	})

	t.Run("missing socket times out", func(t *testing.T) {
		socketPath := filepath.Join(t.TempDir(), "missing.sock")
		calls := 0
		firecrackerSocketProbe = func(string) error {
			calls++
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		err := waitForFirecrackerSocket(ctx, socketPath)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
		if calls != 0 {
			t.Fatalf("expected no socket probe calls for missing socket, got %d", calls)
		}
	})

	t.Run("existing socket with failing probe times out", func(t *testing.T) {
		socketPath := filepath.Join(t.TempDir(), "fc.sock")
		if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
			t.Fatalf("write socket placeholder: %v", err)
		}
		calls := 0
		firecrackerSocketProbe = func(string) error {
			calls++
			return errors.New("not ready")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()

		err := waitForFirecrackerSocket(ctx, socketPath)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
		if calls == 0 {
			t.Fatal("expected probe to be called for existing socket")
		}
	})

	t.Run("existing socket with healthy probe succeeds", func(t *testing.T) {
		socketPath := filepath.Join(t.TempDir(), "fc.sock")
		if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
			t.Fatalf("write socket placeholder: %v", err)
		}
		firecrackerSocketProbe = func(string) error {
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := waitForFirecrackerSocket(ctx, socketPath); err != nil {
			t.Fatalf("waitForFirecrackerSocket returned error: %v", err)
		}
	})
}

func TestStartFirecrackerUnitUsesTransientUnitProperties(t *testing.T) {
	originalConnector := systemdDBusConnector
	originalCloser := systemdDBusCloser
	originalStart := systemdStartTransientUnit
	originalStateReader := systemdUnitStateReader
	originalProbe := firecrackerSocketProbe
	originalBin := firecrackerBinaryPath
	t.Cleanup(func() {
		systemdDBusConnector = originalConnector
		systemdDBusCloser = originalCloser
		systemdStartTransientUnit = originalStart
		systemdUnitStateReader = originalStateReader
		firecrackerSocketProbe = originalProbe
		firecrackerBinaryPath = originalBin
	})

	firecrackerBinaryPath = "/usr/bin/firecracker"
	systemdDBusConnector = func(context.Context) (*systemddb.Conn, error) {
		return &systemddb.Conn{}, nil
	}
	systemdDBusCloser = func(*systemddb.Conn) error { return nil }

	var gotUnit string
	var gotMode string
	var gotProps []systemddb.Property
	systemdStartTransientUnit = func(ctx context.Context, conn *systemddb.Conn, name string, mode string, properties []systemddb.Property, ch chan<- string) (int, error) {
		gotUnit = name
		gotMode = mode
		gotProps = append([]systemddb.Property(nil), properties...)
		ch <- "done"
		return 1, nil
	}
	systemdUnitStateReader = func(string) (systemdUnitState, error) {
		return systemdUnitState{Unit: gotUnit, ActiveState: "active", MainPID: 123}, nil
	}
	firecrackerSocketProbe = func(string) error { return nil }

	socketPath := filepath.Join(t.TempDir(), "fc.sock")
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	machine, err := firecracker.NewMachine(context.Background(), firecracker.Config{
		VMID:       "vm-test",
		SocketPath: socketPath,
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}

	if err := startFirecrackerUnit(context.Background(), machine, "ocm-vm-vm-test.service", socketPath, "vm-test"); err != nil {
		t.Fatalf("startFirecrackerUnit returned error: %v", err)
	}
	if gotUnit != "ocm-vm-vm-test.service" {
		t.Fatalf("expected unit name %q, got %q", "ocm-vm-vm-test.service", gotUnit)
	}
	if gotMode != "replace" {
		t.Fatalf("expected mode replace, got %q", gotMode)
	}
	if len(gotProps) != 6 {
		t.Fatalf("expected 6 transient unit properties, got %d", len(gotProps))
	}
	if gotProps[0].Name != "ExecStart" {
		t.Fatalf("expected first property ExecStart, got %q", gotProps[0].Name)
	}
	if gotProps[1].Name != "Type" || gotProps[2].Name != "Description" {
		t.Fatalf("expected Type and Description properties, got %q and %q", gotProps[1].Name, gotProps[2].Name)
	}
	if gotProps[3].Name != "Restart" || gotProps[4].Name != "KillMode" || gotProps[5].Name != "CollectMode" {
		t.Fatalf("unexpected trailing property order: %q, %q, %q", gotProps[3].Name, gotProps[4].Name, gotProps[5].Name)
	}
}

// TestWaitForSystemdUnitActiveRetriesTransientReadErrors locks in review
// finding #3: a transient dbus read error (GetUnitProperties racing unit
// lookup, private socket momentarily unavailable, etc) must not abort
// boot. The wait loop tolerates a bounded number of consecutive read
// failures and recovers as soon as a read succeeds.
func TestWaitForSystemdUnitActiveRetriesTransientReadErrors(t *testing.T) {
	originalReader := systemdUnitStateReader
	t.Cleanup(func() { systemdUnitStateReader = originalReader })

	t.Run("recovers after transient burst", func(t *testing.T) {
		calls := 0
		systemdUnitStateReader = func(unit string) (systemdUnitState, error) {
			calls++
			// First 3 calls fail transiently; then the unit reports active.
			if calls <= 3 {
				return systemdUnitState{}, errors.New("transient dbus read failure")
			}
			return systemdUnitState{
				Unit:        unit,
				LoadState:   "loaded",
				ActiveState: "active",
				MainPID:     42,
			}, nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		pid, err := waitForSystemdUnitActive(ctx, "ocm-vm-test.service")
		if err != nil {
			t.Fatalf("expected recovery from transient errors, got %v", err)
		}
		if pid != 42 {
			t.Fatalf("pid = %d, want 42", pid)
		}
		if calls != 4 {
			t.Fatalf("expected 4 reader calls (3 failures + 1 success), got %d", calls)
		}
	})

	t.Run("surfaces sustained errors past budget", func(t *testing.T) {
		systemdUnitStateReader = func(string) (systemdUnitState, error) {
			return systemdUnitState{}, errors.New("sustained dbus outage")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		start := time.Now()
		_, err := waitForSystemdUnitActive(ctx, "ocm-vm-test.service")
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected error after budget exceeded")
		}
		if !strings.Contains(err.Error(), "sustained dbus outage") {
			t.Fatalf("expected error to wrap the last transient error, got %v", err)
		}
		if !strings.Contains(err.Error(), "retries") {
			t.Fatalf("expected error to mention retry count, got %v", err)
		}
		// Should surface after (budget+1) reads — well under the 5s deadline.
		if elapsed > 500*time.Millisecond {
			t.Fatalf("budget exhaustion took %v, should be well under 5s deadline", elapsed)
		}
	})

	t.Run("budget resets after a successful read", func(t *testing.T) {
		calls := 0
		systemdUnitStateReader = func(unit string) (systemdUnitState, error) {
			calls++
			switch {
			case calls <= 3:
				// Burst 1: 3 failures (within budget).
				return systemdUnitState{}, errors.New("burst 1 error")
			case calls == 4:
				// Successful read — still activating, budget resets.
				return systemdUnitState{Unit: unit, LoadState: "loaded", ActiveState: "activating"}, nil
			case calls <= 9:
				// Burst 2: 5 failures. Without the reset this would
				// exceed the budget because the counter from burst 1
				// would still be at 3.
				return systemdUnitState{}, errors.New("burst 2 error")
			default:
				return systemdUnitState{Unit: unit, LoadState: "loaded", ActiveState: "active", MainPID: 99}, nil
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		pid, err := waitForSystemdUnitActive(ctx, "ocm-vm-test.service")
		if err != nil {
			t.Fatalf("budget reset failed: %v", err)
		}
		if pid != 99 {
			t.Fatalf("pid = %d, want 99", pid)
		}
	})
}

// TestWaitForSystemdUnitActiveFastFailsOnTerminalStates locks in the H1 fix:
// the wait loop must return immediately when the unit is in a terminal state
// (failed or not-found) rather than burning the caller's full boot deadline.
func TestWaitForSystemdUnitActiveFastFailsOnTerminalStates(t *testing.T) {
	originalReader := systemdUnitStateReader
	t.Cleanup(func() { systemdUnitStateReader = originalReader })

	cases := []struct {
		name       string
		state      systemdUnitState
		wantErrSub string
	}{
		{
			name: "failed state",
			state: systemdUnitState{
				Unit:        "ocm-vm-test.service",
				LoadState:   "loaded",
				ActiveState: "failed",
				SubState:    "failed",
			},
			wantErrSub: "entered failed state",
		},
		{
			name: "unit vanished",
			state: systemdUnitState{
				Unit:        "ocm-vm-test.service",
				LoadState:   "not-found",
				ActiveState: "inactive",
			},
			wantErrSub: "vanished before becoming active",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			systemdUnitStateReader = func(string) (systemdUnitState, error) {
				calls++
				return tc.state, nil
			}

			// Long deadline so a ctx-timeout return would mean we are
			// still draining the full wait (i.e., the fast-fail didn't
			// fire). Real fix should return immediately.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			start := time.Now()
			_, err := waitForSystemdUnitActive(ctx, tc.state.Unit)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("expected waitForSystemdUnitActive to return error for terminal state")
			}
			if !errorsContains(err, tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
			}
			if elapsed > 500*time.Millisecond {
				t.Fatalf("fast-fail took %v, should be well under the 5s deadline", elapsed)
			}
			if calls < 1 {
				t.Fatalf("expected at least one state read, got %d", calls)
			}
		})
	}
}

func errorsContains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

// TestStartFirecrackerUnitRegistersCleanupBeforeBootWait locks in the fix for
// the mid-boot unit-leak bug: a ctx cancel between a successful
// StartTransientUnit and the end of waitForSystemdUnitActive must leave the
// stop-unit cleanup registered on the machine so the firecracker-go-sdk's
// doCleanup() (run on any handler error) still tears the unit back down.
// Before the fix, cleanup was registered *after* the wait, so a mid-boot
// cancel stranded an active ocm-vm-*.service with no in-memory record.
func TestStartFirecrackerUnitRegistersCleanupBeforeBootWait(t *testing.T) {
	originalConnector := systemdDBusConnector
	originalCloser := systemdDBusCloser
	originalStart := systemdStartTransientUnit
	originalStateReader := systemdUnitStateReader
	originalStopper := systemdUnitStopper
	originalBin := firecrackerBinaryPath
	t.Cleanup(func() {
		systemdDBusConnector = originalConnector
		systemdDBusCloser = originalCloser
		systemdStartTransientUnit = originalStart
		systemdUnitStateReader = originalStateReader
		systemdUnitStopper = originalStopper
		firecrackerBinaryPath = originalBin
	})

	firecrackerBinaryPath = "/usr/bin/firecracker"
	systemdDBusConnector = func(context.Context) (*systemddb.Conn, error) {
		return &systemddb.Conn{}, nil
	}
	systemdDBusCloser = func(*systemddb.Conn) error { return nil }
	systemdStartTransientUnit = func(ctx context.Context, conn *systemddb.Conn, name string, mode string, properties []systemddb.Property, ch chan<- string) (int, error) {
		ch <- "done" // systemd accepted the unit
		return 1, nil
	}
	// State reader keeps returning "activating" so waitForSystemdUnitActive
	// spins until the caller's ctx times out, simulating a slow-booting
	// service that a mid-boot cancel would abandon.
	systemdUnitStateReader = func(string) (systemdUnitState, error) {
		return systemdUnitState{Unit: "ocm-vm-vm-test.service", ActiveState: "activating", MainPID: 0}, nil
	}
	stoppedUnits := 0
	var stoppedUnit string
	systemdUnitStopper = func(unit string) error {
		stoppedUnits++
		stoppedUnit = unit
		return nil
	}

	socketPath := filepath.Join(t.TempDir(), "fc.sock")
	if err := os.WriteFile(socketPath, []byte("socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}
	machine, err := firecracker.NewMachine(context.Background(), firecracker.Config{
		VMID:       "vm-test",
		SocketPath: socketPath,
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = startFirecrackerUnit(ctx, machine, "ocm-vm-vm-test.service", socketPath, "vm-test")
	if err == nil {
		t.Fatal("expected startFirecrackerUnit to fail when boot wait times out")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline-exceeded, got %v", err)
	}

	// The fix moves the cleanup append to before StartTransientUnit, so
	// when the wait aborts our stop-unit cleanup is already on the machine.
	funcs := machineCleanupFuncs(machine)
	if len(funcs) == 0 {
		t.Fatal("expected machine.cleanupFuncs to contain the stop-unit cleanup; got empty slice")
	}

	// Invoke the last-registered cleanup (LIFO, matching SDK run order) and
	// verify the stopper fires for our unit. This is what the SDK would do
	// after machine.Start() sees the handler return an error.
	if err := funcs[len(funcs)-1](); err != nil {
		t.Fatalf("cleanup func returned error: %v", err)
	}
	if stoppedUnits != 1 {
		t.Fatalf("expected systemdUnitStopper to fire once, got %d", stoppedUnits)
	}
	if stoppedUnit != "ocm-vm-vm-test.service" {
		t.Fatalf("expected stopper to receive ocm-vm-vm-test.service, got %q", stoppedUnit)
	}
}

func TestSystemdRuntimeOwnerStartHonorsCallerContextDuringBoot(t *testing.T) {
	originalConnector := systemdDBusConnector
	originalCloser := systemdDBusCloser
	originalStart := systemdStartTransientUnit
	originalStopper := systemdUnitStopper
	originalBin := firecrackerBinaryPath
	t.Cleanup(func() {
		systemdDBusConnector = originalConnector
		systemdDBusCloser = originalCloser
		systemdStartTransientUnit = originalStart
		systemdUnitStopper = originalStopper
		firecrackerBinaryPath = originalBin
	})

	firecrackerBinaryPath = "/usr/bin/firecracker"
	systemdDBusConnector = func(context.Context) (*systemddb.Conn, error) {
		return &systemddb.Conn{}, nil
	}
	systemdDBusCloser = func(*systemddb.Conn) error { return nil }
	systemdStartTransientUnit = func(ctx context.Context, conn *systemddb.Conn, name string, mode string, properties []systemddb.Property, ch chan<- string) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	// The mid-boot-cleanup fix now registers a stop-unit func on the
	// machine before StartTransientUnit, so the SDK runs it on any handler
	// error. Stub the stopper so this test doesn't escape to real dbus.
	systemdUnitStopper = func(string) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	kernelPath := filepath.Join(t.TempDir(), "vmlinux")
	rootfsPath := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o600); err != nil {
		t.Fatalf("write kernel placeholder: %v", err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0o600); err != nil {
		t.Fatalf("write rootfs placeholder: %v", err)
	}
	socketPath := filepath.Join(t.TempDir(), "fc.sock")

	owner := systemdUnitRuntimeOwner{}
	_, _, vmCancel, err := owner.start(ctx, "vm-test", false, socketPath, firecracker.Config{
		VMID:            "vm-test",
		SocketPath:      socketPath,
		KernelImagePath: kernelPath,
		Drives: []models.Drive{{
			DriveID:      firecracker.String("rootfs"),
			IsRootDevice: firecracker.Bool(true),
			IsReadOnly:   firecracker.Bool(false),
			PathOnHost:   firecracker.String(rootfsPath),
		}},
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(1),
			MemSizeMib: firecracker.Int64(128),
		},
	}, nil, nil)
	if vmCancel != nil {
		vmCancel()
	}
	if err == nil {
		t.Fatal("expected start to fail when caller context is already canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
