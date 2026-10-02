//go:build !windows

package patchapply

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// TestApply_WriteFailureCleansUpItsOwnNewlyCreatedDirectory covers a gap
// TestApply_PartialFailureRollsBackAllPriorWrites (adversarial_test.go) does
// not: a write failure into a directory chain THIS SAME change created, not
// a pre-existing locked one. A prior version of applyAll dropped the
// CreatedDir the failing entry's own applyWrite call returned, so the new
// directory chain was left on disk even though Apply reported a full
// rollback to the pre-Apply state.
//
// The repro needs RLIMIT_FSIZE (POSIX-only, hence this file's build tag):
// it's the only way to make a write fail *inside* a directory this same
// call just created with the normal 0o755 mode, since any permission-based
// obstacle would make ensureDir itself fail first instead. RLIMIT_FSIZE is
// a process-wide resource limit, though, so capping it in this test's own
// goroutine risks breaking an unrelated write some other goroutine in the
// shared `go test` binary makes concurrently (confirmed empirically: doing
// this in-process broke go test's own testlog.txt write). The repro
// therefore runs in an isolated subprocess via the standard
// TestHelperProcess pattern (as net/http and os/exec's own tests do), so
// the capped limit never touches the parent test binary's process at all.
func TestApply_WriteFailureCleansUpItsOwnNewlyCreatedDirectory(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_WriteFailureInNewDir$", "-test.v=true")
	cmd.Env = append(os.Environ(),
		"PATCHAPPLY_RLIMIT_HELPER=1",
		"PATCHAPPLY_RLIMIT_TARGETDIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper subprocess failed: %v\noutput:\n%s", err, out)
	}
	t.Logf("helper subprocess output:\n%s", out)

	if _, statErr := os.Stat(filepath.Join(dir, "newdir")); !os.IsNotExist(statErr) {
		t.Fatalf("newdir should have been rolled back entirely (never left behind), stat err: %v", statErr)
	}
}

// TestHelperProcess_WriteFailureInNewDir is not a real test. It only runs
// the RLIMIT_FSIZE repro when invoked as a subprocess by
// TestApply_WriteFailureCleansUpItsOwnNewlyCreatedDirectory above (guarded
// by PATCHAPPLY_RLIMIT_HELPER so a normal `go test` run of this package
// never executes any of this body) — see that test's comment for why this
// has to run out-of-process.
func TestHelperProcess_WriteFailureInNewDir(t *testing.T) {
	if os.Getenv("PATCHAPPLY_RLIMIT_HELPER") != "1" {
		return
	}
	dir := os.Getenv("PATCHAPPLY_RLIMIT_TARGETDIR")

	// By default SIGXFSZ terminates the process instead of letting the
	// write syscall that exceeds RLIMIT_FSIZE return EFBIG.
	signal.Ignore(syscall.SIGXFSZ)

	var oldLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
		fmt.Println("FAIL: Getrlimit(RLIMIT_FSIZE):", err)
		os.Exit(1)
	}
	tiny := syscall.Rlimit{Cur: 4, Max: oldLimit.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &tiny); err != nil {
		fmt.Println("FAIL: Setrlimit(RLIMIT_FSIZE):", err)
		os.Exit(1)
	}

	// Capping RLIMIT_FSIZE forces the temp file's content write (inside
	// writeViaRename, called by applyWrite after ensureDir has already
	// created "newdir" and "newdir/sub" for this change) to fail with
	// EFBIG, deterministically, without ever reaching the rename step — so
	// the target path itself is never created, and this failure is
	// attributable solely to the write, not to anything about the target
	// path's name.
	_, applyErr := Apply(dir, []FileChange{
		{Path: "newdir/sub/file.txt", NewContent: []byte("this content is longer than the 4-byte limit")},
	}, ApplyOptions{})

	// Restore before any further I/O in this process (including the
	// fmt.Println calls below), which would otherwise be capped too.
	_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &oldLimit)

	if applyErr == nil {
		fmt.Println("FAIL: expected Apply to fail when the write exceeds RLIMIT_FSIZE")
		os.Exit(1)
	}
	fmt.Println("apply failed as expected:", applyErr)
}
