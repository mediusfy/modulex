# integration: hermetic entry-point smoke tests

Every real `package main` in this repository (examples, `services/prreview`,
`tools/*`) is built with `go build` and run as a child process. The tests assert
what a user or operator can observe: exit code, output, HTTP/gRPC/MCP responses,
and shutdown behaviour. Nothing here needs Docker, cloud credentials or the
network beyond loopback.

## Running

```sh
go test ./integration/...            # builds and runs all entry points (a few seconds warm, a minute cold)
go test -race -count=1 ./integration/...
go test -short ./integration/...     # skips the build-and-run rows (the table-shape checks still run)
go test ./integration/... -run 'TestEntryPoints/receiver'   # one binary
make test-integration                # same as the race run above
```

Binaries are built once per run into a temp directory. Nested modules
(`tools/*`, `services/prreview`, `examples/external-consumer`) are built from
their own module directory with `GOWORK=off`, so their dependencies must be in
the module cache (as for `make check-nested-modules`).

## How it works

- `entrypoints_test.go` holds the table `entryPoints`: one row per
  entry point and scenario (`<binary>/<scenario>`).
- A row has a `setup` func that starts any stubs (HTTP, gRPC, in-process NATS
  with JetStream, fake credentials) and returns a `launch`: args, env, working
  directory, and optional hooks.
- `modeRun` rows run to completion (or until stdin closes) and assert exit code
  and output substrings. `modeServe` rows poll `launch.ready` until the server
  answers, run `launch.exercise`, send SIGTERM, and assert the shutdown (exit
  code, or death by SIGTERM for binaries that do not trap it).
- Children get a minimal environment (see `baseEnv`): nothing from the
  developer's shell leaks in. Free ports come from `freePort`.
- `TestEveryMainPackageHasARow` walks the repository for `package main`
  directories (skipping `testdata`) and fails if one has no row.

## Adding an entry point

1. Add a row to `entryPoints` with `moduleDir` (the module's directory relative
   to the repo root, `.` for the root module), `pkg` (as passed to
   `go build` from that directory) and a `setup` func.
2. Prefer a row for the happy path and separate rows for each config-validation
   failure (`failRow` is a shortcut: expected non-zero exit plus message).
3. Use `freePort` for any port the binary lets you choose. If it hard-codes a
   port, call `requirePort8080` style locking so rows cannot collide, and let
   the row skip when the port is taken.
4. Never use fixed sleeps: poll with `pollUntil` / `httpProbe` / `outputProbe`.

## Known limits

- `hexagonal` and `monolith` hard-code `:8080`. Their rows are serialised and
  skip when something already listens on 8080 (for example Docker Desktop or
  OrbStack port forwards).
- The prreview receiver and worker do not trap SIGTERM, so their rows can only
  assert that SIGTERM ends the process, not a graceful drain
  (`shutdownKilledBySignal`).
- The prreview rows use the self-hosted backends (`STORE_BACKEND=postgres`
  with a lazy DSN that is never queried, `QUEUE_BACKEND=nats` against an
  in-process server). The Firestore, Cloud Tasks and Secret Manager paths, and
  the review pipeline itself, are not exercised.
- Several example binaries log to `io.Discard`, so their failure rows can only
  assert the exit code, not a message.
