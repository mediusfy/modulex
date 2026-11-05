//go:build unix

package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "modulex-integration-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create binary dir:", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// mode says how an entry point is exercised.
type mode int

const (
	// modeRun: the process does its work and exits on its own (or when its
	// stdin closes). The row asserts exit code and output.
	modeRun mode = iota
	// modeServe: the process runs until told to stop. The row asserts
	// readiness, behaviour, and what happens on SIGTERM.
	modeServe
)

// shutdown says what a clean SIGTERM looks like for a server entry point.
type shutdown int

const (
	// shutdownExitZero: the binary traps SIGTERM, drains, and exits with
	// entryPoint.wantExit.
	shutdownExitZero shutdown = iota
	// shutdownKilledBySignal: the binary does not trap SIGTERM; the Go
	// runtime's default action terminates it. This is how prreview's
	// receiver and worker behave today (they call ListenAndServe directly
	// and have no signal handling). The row still asserts the process goes
	// away promptly, but cannot assert a graceful drain.
	shutdownKilledBySignal
)

// entryPoint is one row of the smoke-test table.
type entryPoint struct {
	name      string // "<binary>/<scenario>"
	moduleDir string // directory of the Go module, relative to the repo root
	pkg       string // package path relative to moduleDir, as given to `go build`
	mode      mode

	// setup prepares stubs/fixtures and returns how to launch the binary.
	setup func(t *testing.T) launch

	// wantExit is the expected exit code (modeRun), or the expected exit code
	// after SIGTERM when shutdown == shutdownExitZero (modeServe).
	wantExit int
	// wantOutput lists substrings that must appear in stdout+stderr.
	wantOutput []string
	shutdown   shutdown
}

// ---- row helpers -----------------------------------------------------------

// fixedPort8080 is the address entry points that hard-code ":8080" listen on.
const fixedPort8080 = "http://127.0.0.1:8080"

// failRow builds a modeRun row for a binary that must reject its input with
// exit code wantExit and a message containing wantOutput (when non-empty).
func failRow(name, moduleDir, pkg string, wantExit int, wantOutput string, setup func(t *testing.T) launch) entryPoint {
	row := entryPoint{name: name, moduleDir: moduleDir, pkg: pkg, mode: modeRun, setup: setup, wantExit: wantExit}
	if wantOutput != "" {
		row.wantOutput = []string{wantOutput}
	}
	return row
}

// argsOnly is a setup func for rows that need only arguments and env.
func argsOnly(env []string, args ...string) func(t *testing.T) launch {
	return func(*testing.T) launch { return launch{args: args, env: env} }
}

// fileExists asserts that path exists.
func fileExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.NoError(t, err, "expected %s to have been created", path)
}

// writeFile writes content to dir/name, creating parent directories.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// ---- deployment examples ---------------------------------------------------

func notificationServerSetup(t *testing.T) launch {
	port := strconv.Itoa(freePort(t))
	base := "http://127.0.0.1:" + port
	return launch{
		env:   []string{"PORT=" + port},
		ready: httpProbe("POST", base+"/notify", `{"message":"ready-probe"}`, 202),
		exercise: func(t *testing.T, _ *process) {
			status, _, err := doHTTP("POST", base+"/notify", `{"message":""}`, nil)
			require.NoError(t, err)
			assert.Equal(t, 400, status, "an empty message must be rejected as a client error")
			status, _, err = doHTTP("POST", base+"/notify", `not json`, nil)
			require.NoError(t, err)
			assert.Equal(t, 400, status, "a malformed body must be rejected as a client error")
		},
	}
}

func monolithSetup(t *testing.T) launch {
	requirePort8080(t)
	return launch{
		ready: httpProbe("POST", fixedPort8080+"/notify", `{"message":"ready-probe"}`, 202),
		exercise: func(t *testing.T, _ *process) {
			status, _, err := doHTTP("POST", fixedPort8080+"/notify", `{"message":""}`, nil)
			require.NoError(t, err)
			assert.Equal(t, 400, status, "an empty message must be rejected as a client error")
		},
	}
}

func hexagonalSetup(t *testing.T) launch {
	requirePort8080(t)
	return launch{
		ready: httpProbe("GET", fixedPort8080+"/api/incidents", "", 200),
		exercise: func(t *testing.T, _ *process) {
			status, _, err := doHTTP("POST", fixedPort8080+"/api/incidents",
				`{"title":"smoke incident","severity":"high"}`, map[string]string{"Content-Type": "application/json"})
			require.NoError(t, err)
			require.Equal(t, 201, status, "creating an incident")
			status, body, err := doHTTP("GET", fixedPort8080+"/api/incidents", "", nil)
			require.NoError(t, err)
			require.Equal(t, 200, status)
			assert.Contains(t, body, "smoke incident", "created incident must be listed")
		},
	}
}

func httpConsumerSetup(t *testing.T) launch {
	stub, log := startHTTPNotificationStub(t)
	return launch{
		env: []string{"NOTIFICATION_URL=" + stub.URL},
		after: func(t *testing.T, _ *process) {
			assert.Equal(t, []string{"hello from consumer"}, log.all(), "consumer must send exactly one notification to the remote service")
		},
	}
}

func grpcConsumerSetup(t *testing.T) launch {
	target, log := startGRPCNotificationStub(t)
	return launch{
		args: []string{"-notification-target", target},
		after: func(t *testing.T, _ *process) {
			assert.Equal(t, []string{"hello from consumer"}, log.all(), "consumer must send exactly one notification to the remote gRPC service")
		},
	}
}

func grpcServerSetup(t *testing.T) launch {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	// One client connection, created on first use by the (single-goroutine)
	// ready/exercise sequence and closed at test cleanup.
	var conn *grpc.ClientConn
	connect := func() (*grpc.ClientConn, error) {
		if conn != nil {
			return conn, nil
		}
		c, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		conn = c
		t.Cleanup(func() { _ = c.Close() })
		return c, nil
	}
	return launch{
		env: []string{"GRPC_ADDR=" + addr},
		ready: func(*process) error {
			c, err := connect()
			if err != nil {
				return err
			}
			ctx, cancel := contextWithTimeout(2 * time.Second)
			defer cancel()
			resp, err := healthpb.NewHealthClient(c).Check(ctx, &healthpb.HealthCheckRequest{})
			if err != nil {
				return err
			}
			if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				return fmt.Errorf("health status %s", resp.GetStatus())
			}
			return nil
		},
		exercise: func(t *testing.T, _ *process) {
			c, err := connect()
			require.NoError(t, err)
			client := notificationpb.NewNotificationClient(c)
			ctx, cancel := contextWithTimeout(5 * time.Second)
			defer cancel()
			_, err = client.Send(ctx, &notificationpb.SendRequest{Message: "hello"})
			assert.NoError(t, err, "sending a valid message")
			_, err = client.Send(ctx, &notificationpb.SendRequest{Message: ""})
			assert.Equal(t, codes.InvalidArgument, status.Code(err), "an empty message must map to InvalidArgument")
		},
	}
}

// ---- prreview --------------------------------------------------------------

const (
	prreviewStream    = "PRREVIEW_REVIEWS"
	prreviewConsumer  = "prreview-worker"
	webhookSecret     = "smoke-webhook-secret" // nosecret: smoke-test fixture
	lazyPostgresDSN   = "postgres://smoke:smoke@127.0.0.1:1/smoke?sslmode=disable"
	missingEnvMessage = "missing required environment variable"
)

// receiverEnv is a complete, valid receiver configuration against the
// hermetic backends: the Postgres store (sql.Open is lazy, so a DSN pointing
// nowhere is accepted until the first query, which these rows never make) and
// the NATS queue (a real in-process JetStream server).
func receiverEnv(natsURL string, port int) []string {
	return []string{
		"PORT=" + strconv.Itoa(port),
		"WEBHOOK_SECRET=" + webhookSecret,
		"STORE_BACKEND=postgres",
		"POSTGRES_DSN=" + lazyPostgresDSN,
		"QUEUE_BACKEND=nats",
		"NATS_URL=" + natsURL,
	}
}

// workerEnv is a complete, valid worker configuration against the hermetic
// backends, additionally using fake Application Default Credentials for the
// Secret Manager client constructor and a throwaway GitHub App key.
func workerEnv(t *testing.T, natsURL string, port int) []string {
	return []string{
		"PORT=" + strconv.Itoa(port),
		"GOOGLE_CLOUD_PROJECT=smoke-project",
		"GOOGLE_APPLICATION_CREDENTIALS=" + fakeADCFile(t),
		"GITHUB_APP_ID=12345",
		"GITHUB_APP_PRIVATE_KEY=" + githubAppKeyPEM(t),
		"STORE_BACKEND=postgres",
		"POSTGRES_DSN=" + lazyPostgresDSN,
		"QUEUE_BACKEND=nats",
		"NATS_URL=" + natsURL,
		"NATS_STREAM=" + prreviewStream,
		"NATS_CONSUMER=" + prreviewConsumer,
		"NATS_WORKER_CONCURRENCY=2",
	}
}

func signWebhook(body string) string {
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// jetStreamFor connects to natsURL and returns a JetStream context.
func jetStreamFor(t *testing.T, natsURL string) nats.JetStreamContext {
	t.Helper()
	conn, err := nats.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)
	return js
}

func receiverSetup(t *testing.T) launch {
	natsURL := startNATS(t)
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	return launch{
		env:   receiverEnv(natsURL, port),
		ready: httpProbe("GET", base+"/health", "", 200),
		exercise: func(t *testing.T, _ *process) {
			_, err := jetStreamFor(t, natsURL).StreamInfo(prreviewStream)
			assert.NoError(t, err, "receiver must ensure the review stream exists on the queue backend")

			status, _, err := doHTTP("GET", base+"/webhook", "", nil)
			require.NoError(t, err)
			assert.Equal(t, 405, status, "webhook accepts POST only")

			body := `{"zen":"smoke"}`
			status, _, err = doHTTP("POST", base+"/webhook", body, map[string]string{
				"X-Hub-Signature-256": "sha256=" + strings.Repeat("0", 64),
				"X-GitHub-Event":      "ping",
				"X-GitHub-Delivery":   "smoke-bad-signature",
			})
			require.NoError(t, err)
			assert.Equal(t, 401, status, "a delivery with a wrong HMAC must be rejected")

			status, _, err = doHTTP("POST", base+"/webhook", body, map[string]string{
				"X-Hub-Signature-256": signWebhook(body),
				"X-GitHub-Event":      "ping",
				"X-GitHub-Delivery":   "smoke-ping",
			})
			require.NoError(t, err)
			assert.Equal(t, 200, status, "a correctly signed event the service does not review is acked")
		},
	}
}

func workerSetup(t *testing.T) launch {
	natsURL := startNATS(t)
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	return launch{
		env:   workerEnv(t, natsURL, port),
		ready: httpProbe("GET", base+"/health", "", 200),
		exercise: func(t *testing.T, _ *process) {
			js := jetStreamFor(t, natsURL)
			_, err := js.StreamInfo(prreviewStream)
			assert.NoError(t, err, "worker must ensure the review stream exists")
			_, err = js.ConsumerInfo(prreviewStream, prreviewConsumer)
			assert.NoError(t, err, "worker must have created its durable consumer before reporting healthy")
		},
	}
}

// receiverFail and workerFail launch the binary with a valid config except
// for the given overrides, applied last (the last duplicate env entry wins).
// They never start NATS: NATS_URL points at a port nobody listens on, so a row
// that gets as far as connecting fails with "nats connect".
func receiverFail(overrides ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		env := receiverEnv("nats://127.0.0.1:"+strconv.Itoa(freePort(t)), freePort(t))
		return launch{env: append(env, overrides...)}
	}
}

func workerFail(overrides ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		env := workerEnv(t, "nats://127.0.0.1:"+strconv.Itoa(freePort(t)), freePort(t))
		return launch{env: append(env, overrides...)}
	}
}

// ---- tools -----------------------------------------------------------------

// writeBoundaryFixture writes a tiny module whose orders package imports
// either billing's ports package (allowed) or its service package (a boundary
// violation), and returns its directory.
func writeBoundaryFixture(t *testing.T, violating bool) string {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/app\n\ngo 1.21\n")
	writeFile(t, dir, "modules/billing/ports/ports.go", "package ports\n\ntype Biller interface{ Bill() }\n")
	writeFile(t, dir, "modules/billing/service/service.go", "package service\n\nfunc Bill() {}\n")
	imp, use := "example.com/app/modules/billing/ports", "var _ ports.Biller\n"
	if violating {
		imp, use = "example.com/app/modules/billing/service", "var _ = service.Bill\n"
	}
	writeFile(t, dir, "modules/orders/orders.go", "package orders\n\nimport \""+imp+"\"\n\n"+use)
	return dir
}

func boundarySetup(violating bool, prefix ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		args := append(append([]string{}, prefix...), "-root", "example.com/app/modules", "./...")
		return launch{args: args, dir: writeBoundaryFixture(t, violating)}
	}
}

// scaffoldSetup runs a scaffold-style CLI (scaffold, or `modulex new module`)
// into a temp dir and asserts the generated feature module's layout.
func scaffoldSetup(prefix ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		dir := t.TempDir()
		args := append(append([]string{}, prefix...),
			"-name", "billing", "-out", filepath.Join(dir, "examples"), "-module", "example.com/m/examples")
		return launch{
			args: args,
			after: func(t *testing.T, _ *process) {
				for _, f := range []string{"module.go", "domain", "ports", "service", "adapters"} {
					fileExists(t, filepath.Join(dir, "examples", "billing", f))
				}
			},
		}
	}
}

// contractSetup puts a modulex.agent.yaml into a temp root: the repository's
// own contract when content is empty, otherwise content verbatim. The args are
// followed by -root <that dir>.
func contractSetup(content string, args ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		root := t.TempDir()
		body := content
		if body == "" {
			data, err := os.ReadFile(filepath.Join(repoRoot, "modulex.agent.yaml"))
			require.NoError(t, err)
			body = string(data)
		}
		writeFile(t, root, "modulex.agent.yaml", body)
		return launch{
			args: append(append([]string{}, args...), "-root", root),
			after: func(t *testing.T, _ *process) {
				if len(args) >= 2 && args[1] == "generate" {
					fileExists(t, filepath.Join(root, "AGENTS.md"))
					fileExists(t, filepath.Join(root, "CLAUDE.md"))
				}
			},
		}
	}
}

func doctorJSONSetup(t *testing.T) launch {
	root := t.TempDir()
	return launch{
		args: []string{"doctor", "-json", "-root", root},
		after: func(t *testing.T, p *process) {
			var rep struct {
				Root            string `json:"root"`
				ContractPresent bool   `json:"contract_present"`
			}
			require.NoError(t, json.Unmarshal([]byte(p.stdout.String()), &rep), "stdout must be one JSON document")
			assert.Equal(t, root, rep.Root)
			assert.False(t, rep.ContractPresent, "an empty directory has no contract")
		},
	}
}

func provenanceSetup(args ...string) func(t *testing.T) launch {
	return func(t *testing.T) launch {
		dir := t.TempDir()
		out := filepath.Join(dir, "prov.json")
		return launch{
			args: append(append([]string{}, args...), "-repo", dir, "-out", out),
			after: func(t *testing.T, p *process) {
				if p.exitCode() != 0 {
					return
				}
				data, err := os.ReadFile(out)
				require.NoError(t, err, "envelope file must be written")
				var env struct {
					Repository   struct{ Commit, Branch string }
					Verification []struct{ Name, Status string }
				}
				require.NoError(t, json.Unmarshal(data, &env))
				assert.Equal(t, "main", env.Repository.Branch)
				assert.Len(t, env.Verification, 2, "one verification result per -job")
			},
		}
	}
}

const sampleCommit = "0123456789abcdef0123456789abcdef01234567"

// mcpHandshake drives the MCP stdio protocol: initialize, then tools/list.
func mcpHandshake(t *testing.T, p *process) {
	send := func(msg string) {
		_, err := p.stdin.Write([]byte(msg + "\n"))
		require.NoError(t, err)
	}
	await := func(id int) map[string]any {
		var found map[string]any
		pollUntil(t, p, readyTimeout, fmt.Sprintf("JSON-RPC response %d", id), func() error {
			for _, line := range strings.Split(p.stdout.String(), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) == nil && m["id"] == float64(id) {
					found = m
					return nil
				}
			}
			return fmt.Errorf("no response with id %d yet", id)
		})
		return found
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`)
	initResp := await(1)
	result, _ := initResp["result"].(map[string]any)
	require.NotNil(t, result, "initialize must return a result: %v", initResp)
	info, _ := result["serverInfo"].(map[string]any)
	assert.NotEmpty(t, info["name"], "initialize result must carry serverInfo.name")

	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := await(2)
	result, _ = list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	var names []string
	for _, tool := range tools {
		if m, ok := tool.(map[string]any); ok {
			names = append(names, fmt.Sprint(m["name"]))
		}
	}
	assert.Subset(t, names, []string{"read_contract", "discover_repository"}, "tools/list must advertise the documented tools")
}

// ---- the table -------------------------------------------------------------

// entryPoints has at least one row per real `package main` in the repository
// (TestEveryMainPackageHasARow enforces that) plus rows for config validation
// and error paths. To add an entry point, see README.md.
var entryPoints = []entryPoint{
	// examples (root module)
	{
		name: "bootstrap/starts-and-exits-zero-on-sigterm", moduleDir: ".", pkg: "./examples/bootstrap", mode: modeServe,
		setup: func(*testing.T) launch {
			// app.Run blocks until a signal; the greeting is logged by the
			// module's Start, so it is the readiness signal.
			return launch{ready: outputProbe("hello from modulex/app")}
		},
		wantExit: 0, wantOutput: []string{"hello from modulex/app"},
	},
	{
		name: "hexagonal/serves-incident-api-and-shuts-down", moduleDir: ".", pkg: "./examples/hexagonal", mode: modeServe,
		setup: hexagonalSetup, wantExit: 0, wantOutput: []string{"teardown complete"},
	},
	{
		name: "quickstart/runs-lifecycle-to-completion", moduleDir: ".", pkg: "./examples/quickstart", mode: modeRun,
		setup: argsOnly(nil), wantExit: 0, wantOutput: []string{"greeter started"},
	},
	{
		name: "external-consumer/resolves-service-in-own-module", moduleDir: "examples/external-consumer", pkg: ".", mode: modeRun,
		setup: argsOnly(nil), wantExit: 0, wantOutput: []string{"hello from an external consumer"},
	},

	// examples/deployment (root module)
	{
		name: "monolith/serves-notify-and-shuts-down", moduleDir: ".", pkg: "./examples/deployment/monolith", mode: modeServe,
		setup: monolithSetup, wantExit: 0,
	},
	{
		name: "notification-server/serves-notify-and-shuts-down", moduleDir: ".", pkg: "./examples/deployment/remote/notification-server", mode: modeServe,
		setup: notificationServerSetup, wantExit: 0,
	},
	{
		name: "notification-grpc-server/serves-grpc-and-shuts-down", moduleDir: ".", pkg: "./examples/deployment/remote/notification-grpc-server", mode: modeServe,
		setup: grpcServerSetup, wantExit: 0,
	},
	failRow("notification-grpc-server/unbindable-address-exits-1", ".", "./examples/deployment/remote/notification-grpc-server", 1, "",
		argsOnly([]string{"GRPC_ADDR=256.256.256.256:1"})),
	{
		name: "consumer/sends-notification-to-remote-http-service", moduleDir: ".", pkg: "./examples/deployment/remote/consumer", mode: modeRun,
		setup: httpConsumerSetup, wantExit: 0,
	},
	failRow("consumer/missing-notification-url-exits-1", ".", "./examples/deployment/remote/consumer", 1, "",
		argsOnly(nil)),
	{
		name: "grpc-consumer/sends-notification-to-remote-grpc-service", moduleDir: ".", pkg: "./examples/deployment/remote/grpc-consumer", mode: modeRun,
		setup: grpcConsumerSetup, wantExit: 0,
	},
	failRow("grpc-consumer/missing-notification-target-exits-1", ".", "./examples/deployment/remote/grpc-consumer", 1, "",
		argsOnly(nil)),

	// services/prreview (module services/prreview)
	{
		name: "receiver/serves-health-and-webhook-with-nats-and-postgres", moduleDir: "services/prreview", pkg: "./cmd/receiver", mode: modeServe,
		setup: receiverSetup, shutdown: shutdownKilledBySignal, wantOutput: []string{"receiver listening"},
	},
	failRow("receiver/missing-webhook-secret-exits-1", "services/prreview", "./cmd/receiver", 1, missingEnvMessage+`","name":"WEBHOOK_SECRET`,
		receiverFail("WEBHOOK_SECRET=")),
	failRow("receiver/unknown-store-backend-exits-1", "services/prreview", "./cmd/receiver", 1, "unknown STORE_BACKEND",
		receiverFail("STORE_BACKEND=mysql")),
	failRow("receiver/postgres-without-dsn-exits-1", "services/prreview", "./cmd/receiver", 1, `"name":"POSTGRES_DSN"`,
		receiverFail("POSTGRES_DSN=")),
	failRow("receiver/firestore-without-project-exits-1", "services/prreview", "./cmd/receiver", 1, `"name":"GOOGLE_CLOUD_PROJECT"`,
		receiverFail("STORE_BACKEND=firestore")),
	failRow("receiver/unknown-queue-backend-exits-1", "services/prreview", "./cmd/receiver", 1, "unknown QUEUE_BACKEND",
		receiverFail("QUEUE_BACKEND=sqs")),
	failRow("receiver/unreachable-nats-exits-1", "services/prreview", "./cmd/receiver", 1, "nats connect",
		receiverFail()),
	{
		name: "worker/serves-health-and-consumes-from-nats", moduleDir: "services/prreview", pkg: "./cmd/worker", mode: modeServe,
		setup: workerSetup, shutdown: shutdownKilledBySignal, wantOutput: []string{"worker consuming"},
	},
	failRow("worker/missing-google-cloud-project-exits-1", "services/prreview", "./cmd/worker", 1, `"name":"GOOGLE_CLOUD_PROJECT"`,
		workerFail("GOOGLE_CLOUD_PROJECT=")),
	failRow("worker/invalid-app-private-key-exits-1", "services/prreview", "./cmd/worker", 1, "app private key",
		workerFail("GITHUB_APP_PRIVATE_KEY=not-a-pem")),
	failRow("worker/unknown-store-backend-exits-1", "services/prreview", "./cmd/worker", 1, "unknown STORE_BACKEND",
		workerFail("STORE_BACKEND=mysql")),
	failRow("worker/missing-github-app-id-exits-1", "services/prreview", "./cmd/worker", 1, `"name":"GITHUB_APP_ID"`,
		workerFail("GITHUB_APP_ID=")),
	failRow("worker/unknown-queue-backend-exits-1", "services/prreview", "./cmd/worker", 1, "unknown QUEUE_BACKEND",
		workerFail("QUEUE_BACKEND=sqs")),
	failRow("worker/unreachable-nats-exits-1", "services/prreview", "./cmd/worker", 1, "nats connect",
		workerFail()),

	// tools/agentcli: the unified modulex CLI
	failRow("modulex/no-args-prints-usage-exit-2", "tools/agentcli", "./cmd/modulex", 2, "Usage: modulex <command>",
		argsOnly(nil)),
	failRow("modulex/unknown-command-exit-2", "tools/agentcli", "./cmd/modulex", 2, `unknown command "bogus"`,
		argsOnly(nil, "bogus")),
	failRow("modulex/agent-review-requires-base-exit-2", "tools/agentcli", "./cmd/modulex", 2, "-base is required",
		argsOnly(nil, "agent", "review")),
	failRow("modulex/new-module-requires-name-exit-2", "tools/agentcli", "./cmd/modulex", 2, "-name and -out are required",
		argsOnly(nil, "new", "module")),
	failRow("modulex/check-without-boundary-exit-2", "tools/agentcli", "./cmd/modulex", 2, "Usage: modulex check boundary",
		argsOnly(nil, "check")),
	{
		name: "modulex/doctor-reports-absent-contract", moduleDir: "tools/agentcli", pkg: "./cmd/modulex", mode: modeRun,
		setup:    func(t *testing.T) launch { return launch{args: []string{"doctor", "-root", t.TempDir()}} },
		wantExit: 0, wantOutput: []string{"contract:", "absent"},
	},
	{
		name: "modulex/doctor-json-emits-report", moduleDir: "tools/agentcli", pkg: "./cmd/modulex", mode: modeRun,
		setup: doctorJSONSetup, wantExit: 0,
	},
	failRow("modulex/doctor-broken-contract-exit-1", "tools/agentcli", "./cmd/modulex", 1, "BROKEN",
		contractSetup(":::not yaml [\n", "doctor")),
	{
		name: "modulex/agent-generate-writes-agent-docs", moduleDir: "tools/agentcli", pkg: "./cmd/modulex", mode: modeRun,
		setup: contractSetup("", "agent", "generate"), wantExit: 0, wantOutput: []string{"wrote AGENTS.md", "wrote CLAUDE.md"},
	},
	{
		name: "modulex/new-module-scaffolds-feature-module", moduleDir: "tools/agentcli", pkg: "./cmd/modulex", mode: modeRun,
		setup: scaffoldSetup("new", "module"), wantExit: 0, wantOutput: []string{"generated"},
	},
	failRow("modulex/check-boundary-flags-violation-exit-3", "tools/agentcli", "./cmd/modulex", 3, "billing/service",
		boundarySetup(true, "check", "boundary")),
	{
		name: "modulex/check-boundary-accepts-ports-import", moduleDir: "tools/agentcli", pkg: "./cmd/modulex", mode: modeRun,
		setup: boundarySetup(false, "check", "boundary"), wantExit: 0,
	},

	// tools/mcpserver
	{
		name: "mcpserver/completes-mcp-handshake-over-stdio", moduleDir: "tools/mcpserver", pkg: "./cmd/mcpserver", mode: modeRun,
		setup:    func(*testing.T) launch { return launch{stdin: true, interact: mcpHandshake} },
		wantExit: 0,
	},

	// tools/modboundary
	{
		name: "modboundary/accepts-ports-import", moduleDir: "tools/modboundary", pkg: "./cmd/modboundary", mode: modeRun,
		setup: boundarySetup(false), wantExit: 0,
	},
	failRow("modboundary/flags-cross-module-import-exit-3", "tools/modboundary", "./cmd/modboundary", 3, "billing/service",
		boundarySetup(true)),

	// tools/provenanceci
	{
		name: "provenanceci/writes-envelope", moduleDir: "tools/provenanceci", pkg: "./cmd/provenanceci", mode: modeRun,
		setup:    provenanceSetup("-commit", sampleCommit, "-branch", "main", "-job", "build=success", "-job", "lint=failure"),
		wantExit: 0, wantOutput: []string{"wrote", "2 job result(s)"},
	},
	failRow("provenanceci/missing-commit-exit-2", "tools/provenanceci", "./cmd/provenanceci", 2, "-commit is required",
		provenanceSetup("-job", "build=success")),
	failRow("provenanceci/missing-job-exit-2", "tools/provenanceci", "./cmd/provenanceci", 2, "at least one -job is required",
		provenanceSetup("-commit", sampleCommit)),
	failRow("provenanceci/malformed-job-exit-2", "tools/provenanceci", "./cmd/provenanceci", 2, "want name=result",
		provenanceSetup("-commit", sampleCommit, "-job", "bogus")),

	// tools/scaffold
	{
		name: "scaffold/generates-feature-module", moduleDir: "tools/scaffold", pkg: "./cmd/scaffold", mode: modeRun,
		setup: scaffoldSetup(), wantExit: 0, wantOutput: []string{"generated"},
	},
	failRow("scaffold/missing-name-exit-2", "tools/scaffold", "./cmd/scaffold", 2, "Usage:",
		argsOnly(nil)),
}

// TestEntryPoints builds and runs every row of the entryPoints table.
func TestEntryPoints(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs every entry point binary; skipped with -short")
	}
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			bin := buildBinary(t, ep.moduleDir, ep.pkg)
			l := ep.setup(t)
			p := startProcess(t, bin, l)

			switch ep.mode {
			case modeRun:
				if l.interact != nil {
					l.interact(t, p)
				}
				if p.stdin != nil {
					require.NoError(t, p.stdin.Close())
				}
				p.waitExit(t)
				assert.Equal(t, ep.wantExit, p.exitCode(), "exit code")
			case modeServe:
				pollUntil(t, p, readyTimeout, "the server to become ready", func() error { return l.ready(p) })
				if l.exercise != nil {
					l.exercise(t, p)
				}
				p.terminate(t)
				switch ep.shutdown {
				case shutdownExitZero:
					assert.Equal(t, ep.wantExit, p.exitCode(), "exit code after SIGTERM (graceful shutdown)")
				case shutdownKilledBySignal:
					assert.Equal(t, syscall.SIGTERM, p.termSignal(), "process must end because of SIGTERM")
				}
			}
			for _, want := range ep.wantOutput {
				assert.Contains(t, p.output(), want, "process output")
			}
			if l.after != nil {
				l.after(t, p)
			}
		})
	}
}
