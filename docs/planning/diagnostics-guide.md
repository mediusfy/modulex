# Modulex Diagnostics Guide

This guide covers `Manager.ModuleContract` and `Manager.Diagnostics`: two
machine-readable, JSON-marshalable exports of a manager's state, intended for
logging, support tickets, and diffing module topology across deployments.

Both are read-only snapshots. Calling them does not mutate manager state and
is safe to call from any lifecycle state, including before `InitModules` has
run.

## ModuleContract

`ModuleContract` describes the registered modules and their declared
dependency edges:

```go
type ModuleContract struct {
    Modules []ModuleContractEntry `json:"modules"`
}

type ModuleContractEntry struct {
    Name      string   `json:"name"`
    DependsOn []string `json:"depends_on"`
}

func (m *Manager) ModuleContract() ModuleContract
```

Modules are sorted alphabetically by name, and each module's `DependsOn` list
is sorted alphabetically as well. This makes `ModuleContract()` produce
byte-identical JSON across repeated calls against the same manager state, so
it can be committed to source control or compared between two versions of an
application to detect real topology changes rather than incidental map or
slice ordering noise.

This is a different, purpose-built export from the existing
`Manager.ExportDAG`, which renders a Mermaid graph for human-readable
documentation. Use `ExportDAG` for diagrams; use `ModuleContract` for
anything that needs to be parsed or diffed programmatically.

```go
contract := manager.ModuleContract()
b, err := json.Marshal(contract)
if err != nil {
    return err
}
fmt.Println(string(b))
// {"modules":[{"name":"billing","depends_on":["accounts"]},{"name":"accounts","depends_on":[]}]}
```

## Diagnostics

`Diagnostics` is a broader snapshot of the manager's runtime state:

```go
type Diagnostics struct {
    State           string           `json:"state"`
    Modules         ModuleContract   `json:"modules"`
    Services        []string         `json:"services"`
    Tasks           []TaskDiagnostic `json:"tasks"`
    HealthChecks    []string         `json:"health_checks"`
    ReadinessChecks []string         `json:"readiness_checks"`
    Timings         LifecycleTimings `json:"timings,omitempty"`
}

type TaskDiagnostic struct {
    Name string `json:"name"`
    Done bool   `json:"done"`
    Err  string `json:"error,omitempty"`
}

type LifecycleTimings struct {
    InitModules  time.Duration  `json:"init_modules_ns,omitempty"`
    StartModules time.Duration  `json:"start_modules_ns,omitempty"`
    ModuleInit   []ModuleTiming `json:"module_init,omitempty"`
    ModuleStart  []ModuleTiming `json:"module_start,omitempty"`
}

type ModuleTiming struct {
    Name       string        `json:"name"`
    DurationNs time.Duration `json:"duration_ns"`
}

func (m *Manager) Diagnostics() Diagnostics
```

### Safe to log or attach to a support ticket

`Diagnostics` is designed to be safe to export, log, or paste into a support
ticket without a secrets review:

- `Services` contains only the sorted **names** under which services were
  registered via `RegisterService`. The registered values themselves — which
  may be database clients, API keys, or other arbitrary application state —
  are never included.
- `HealthChecks` and `ReadinessChecks` contain only the sorted names of
  registered checks, never the check function bodies or anything they might
  close over.
- `Tasks` contains only each supervised task's name, whether it has finished,
  and its final error as a string (`TaskDiagnostic.Err`) — the same
  information already available today via `TaskHandle.Wait`. No task
  closures, contexts, or internal manager synchronization state is exposed.
- `Timings` contains only durations, never module internals.

If you need finer-grained provenance or a versioned export schema, see
MOD-66 (out of scope for this guide).

### Lifecycle timings

`Timings` reports how long the `InitModules` and `StartModules` phases took,
both in total and per module, once those phases have run:

- `InitModules` / `StartModules` are the total wall-clock duration of the
  respective phase, as a `time.Duration` (JSON-encoded as nanoseconds).
- `ModuleInit` / `ModuleStart` list each module's individual duration for that
  phase, sorted alphabetically by module name.

Timings are zero-valued (and, for the per-module slices, `nil`/omitted from
JSON) until the corresponding phase has actually run — they are never
fabricated. A phase that fails partway through still records durations for
every module that was attempted before the failure.

### Task visibility

`Tasks` reflects the manager's currently tracked supervised tasks (started via
`Registry.Go`). A task that has already finished is removed from the
manager's internal tracking before its `TaskHandle` reports completion, so a
finished task will not appear in `Tasks` at all — this mirrors the manager's
existing behavior for `StopModules`, which is the single source of truth for
already-collected task errors. A task that is still running appears with
`Done: false` and an empty `Err`.

### Example

```go
diag := manager.Diagnostics()
b, err := json.Marshal(diag)
if err != nil {
    return err
}
logger.Info("modulex diagnostics", slog.String("diagnostics", string(b)))
```

Example output (formatted for readability):

```json
{
  "state": "running",
  "modules": {
    "modules": [
      {"name": "accounts", "depends_on": []},
      {"name": "billing", "depends_on": ["accounts"]}
    ]
  },
  "services": ["accounts.Repository", "billing.Service"],
  "tasks": [
    {"name": "billing.reconciler", "done": false}
  ],
  "health_checks": ["accounts.db"],
  "readiness_checks": ["billing.upstream"],
  "timings": {
    "init_modules_ns": 1234567,
    "start_modules_ns": 456789,
    "module_init": [
      {"name": "accounts", "duration_ns": 600000},
      {"name": "billing", "duration_ns": 634567}
    ],
    "module_start": [
      {"name": "accounts", "duration_ns": 200000},
      {"name": "billing", "duration_ns": 256789}
    ]
  }
}
```
