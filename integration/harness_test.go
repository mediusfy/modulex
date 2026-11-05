//go:build unix

package integration_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// buildTimeout bounds one `go build` of an entry point (cold caches and a
	// -race test run on a busy CI machine are slow).
	buildTimeout = 5 * time.Minute
	// readyTimeout bounds how long a server may take to become ready.
	readyTimeout = 30 * time.Second
	// exitTimeout bounds how long a process may take to exit after it was
	// asked to (SIGTERM, or end of its run-to-completion work).
	exitTimeout = 30 * time.Second
	// maxParallelBuilds caps concurrent `go build` invocations.
	maxParallelBuilds = 4
)

// repoRoot is the root of the root module (the directory above integration/).
var repoRoot = func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("integration: cannot determine harness source location")
	}
	return filepath.Dir(filepath.Dir(file))
}()

// ---- building --------------------------------------------------------------

var (
	binDir string // created by TestMain, removed after the run

	buildMu    sync.Mutex
	buildCache = map[string]*buildEntry{}
	buildSem   = make(chan struct{}, maxParallelBuilds)
)

type buildEntry struct {
	once sync.Once
	path string
	err  error
}

// buildBinary builds package pkg of the module in moduleDir (relative to the
// repo root) once per test run and returns the binary path. It fails the test
// if the entry point does not build.
func buildBinary(t *testing.T, moduleDir, pkg string) string {
	t.Helper()
	key := moduleDir + "|" + pkg

	buildMu.Lock()
	e, ok := buildCache[key]
	if !ok {
		e = &buildEntry{}
		buildCache[key] = e
	}
	index := len(buildCache)
	buildMu.Unlock()

	e.once.Do(func() {
		buildSem <- struct{}{}
		defer func() { <-buildSem }()

		out := filepath.Join(binDir, fmt.Sprintf("%02d-%s", index, filepath.Base(pkg)))
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir = filepath.Join(repoRoot, moduleDir)
		// GOWORK=off: build exactly the module in cmd.Dir, as CI does.
		cmd.Env = append(os.Environ(), "GOWORK=off")
		done := make(chan struct{})
		var output []byte
		var err error
		go func() {
			output, err = cmd.CombinedOutput()
			close(done)
		}()
		select {
		case <-done:
			if err != nil {
				e.err = fmt.Errorf("go build %s (module %s): %w\n%s", pkg, moduleDir, err, output)
				return
			}
			e.path = out
		case <-time.After(buildTimeout):
			_ = cmd.Process.Kill()
			<-done
			e.err = fmt.Errorf("go build %s (module %s): timed out after %s", pkg, moduleDir, buildTimeout)
		}
	})
	if e.err != nil {
		t.Fatalf("entry point does not build: %v", e.err)
	}
	return e.path
}

// ---- running ---------------------------------------------------------------

// syncBuffer is a bytes.Buffer safe for one writer goroutine (os/exec's copy
// goroutine) and concurrent readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// launch describes how one entry-point row is started and exercised. It is
// produced by the row's setup function, which may start stubs (HTTP, gRPC,
// NATS) whose addresses end up in args/env and whose recordings are inspected
// by the closures.
type launch struct {
	args []string
	// env is added to the minimal base environment (see baseEnv).
	env []string
	dir string // working directory; empty means a fresh temp dir
	// stdin, when true, connects a pipe the interact hook can write to;
	// otherwise stdin is the null device.
	stdin bool

	// ready probes a server (modeServe). It is polled until it returns nil.
	ready func(p *process) error
	// exercise runs after ready succeeded and before shutdown (modeServe).
	exercise func(t *testing.T, p *process)
	// interact runs right after start while the process is alive (modeRun),
	// e.g. to drive a stdio protocol. The harness closes stdin afterwards.
	interact func(t *testing.T, p *process)
	// after runs once the process has exited (both modes).
	after func(t *testing.T, p *process)
}

// process is a running child process.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{} // closed after cmd.Wait returned
}

// output returns stdout and stderr, concatenated.
func (p *process) output() string { return p.stdout.String() + p.stderr.String() }

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// baseEnv is the deliberately small environment every child starts with:
// nothing from the developer's shell (cloud credentials, PORT, NATS_URL, ...)
// leaks in. The Go and PATH variables are what the `go` tool, invoked by some
// entry points (modboundary), needs to work.
func baseEnv(t *testing.T) []string {
	t.Helper()
	env := []string{"TMPDIR=" + t.TempDir(), "GOWORK=off", "GOTOOLCHAIN=local"}
	for _, name := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "GOPROXY", "GOSUMDB", "GONOSUMDB"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// startProcess starts bin and registers cleanup that kills it if the test
// forgot to stop it and prints its output when the test failed.
func startProcess(t *testing.T, bin string, l launch) *process {
	t.Helper()
	dir := l.dir
	if dir == "" {
		dir = t.TempDir()
	}
	cmd := exec.Command(bin, l.args...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv(t), l.env...)
	p := &process{cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout = p.stdout
	cmd.Stderr = p.stderr
	if l.stdin {
		w, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		p.stdin = w
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", filepath.Base(bin), err)
	}
	// Registered first so it runs last (t.Cleanup is LIFO): the output is
	// complete once the kill cleanup below has reaped the process.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("process %s\nargs: %q\nstdout:\n%s\nstderr:\n%s",
				filepath.Base(bin), l.args, p.stdout.String(), p.stderr.String())
		}
	})
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		if !p.exited() {
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// waitExit waits for the process to exit by itself within exitTimeout.
func (p *process) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(exitTimeout):
		t.Fatalf("process did not exit within %s", exitTimeout)
	}
}

// exitCode returns the exit code, or -1 if the process died from a signal.
func (p *process) exitCode() int { return p.cmd.ProcessState.ExitCode() }

// termSignal returns the signal that killed the process, or 0.
func (p *process) termSignal() syscall.Signal {
	ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

// terminate sends SIGTERM and waits for the process to go away.
func (p *process) terminate(t *testing.T) {
	t.Helper()
	if p.exited() {
		t.Fatalf("process exited before it was asked to terminate (exit code %d)", p.exitCode())
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	p.waitExit(t)
}

// pollUntil calls fn every 25ms until it returns nil. It fails the test with
// the last error when the deadline passes or when the process exits first.
func pollUntil(t *testing.T, p *process, timeout time.Duration, what string, fn func() error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		if last = fn(); last == nil {
			return
		}
		select {
		case <-p.done:
			t.Fatalf("process exited (code %d) before %s: last probe error: %v", p.exitCode(), what, last)
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for %s: last probe error: %v", timeout, what, last)
		case <-tick.C:
		}
	}
}

// outputProbe returns a ready func that succeeds once the process has printed
// substr on stdout or stderr.
func outputProbe(substr string) func(*process) error {
	return func(p *process) error {
		if strings.Contains(p.output(), substr) {
			return nil
		}
		return fmt.Errorf("output does not contain %q yet", substr)
	}
}

// ---- stub and probe helpers ------------------------------------------------

// freePort returns a TCP port on 127.0.0.1 that was free a moment ago. There
// is an unavoidable window between releasing and the child binding it; in
// practice the kernel does not hand a just-released ephemeral port out again
// that quickly.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

var probeClient = &http.Client{Timeout: 2 * time.Second}

// httpProbe returns a ready func that succeeds once url answers with want.
func httpProbe(method, url, body string, want int) func(*process) error {
	return func(*process) error {
		status, _, err := doHTTP(method, url, body, nil)
		if err != nil {
			return err
		}
		if status != want {
			return fmt.Errorf("%s %s: status %d, want %d", method, url, status, want)
		}
		return nil
	}
}

// doHTTP performs one request and returns status and body.
func doHTTP(method, url, body string, header map[string]string) (int, string, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(b), nil
}

// port8080 serialises the entry points that hard-code :8080.
var port8080 sync.Mutex

// requirePort8080 takes the :8080 lock for the rest of the test, skipping the
// test if something else on this machine already listens there.
func requirePort8080(t *testing.T) {
	t.Helper()
	port8080.Lock()
	t.Cleanup(port8080.Unlock)
	l, err := net.Listen("tcp", ":8080")
	if err != nil {
		t.Skipf("this entry point hard-codes :8080 and the port is not available: %v", err)
	}
	_ = l.Close()
}
