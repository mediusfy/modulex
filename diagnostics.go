package modulex

import (
	"context"
	"sort"
	"time"
)

// ModuleContract is a machine-readable description of the registered modules
// and their declared dependency edges, independent of the Mermaid-oriented
// ExportDAG. It is intended for diffing between two versions of a running
// application's module topology (e.g. in CI, or between deployments), so its
// JSON encoding is fully deterministic: two calls to Manager.ModuleContract
// against the same manager state always marshal to byte-identical JSON.
type ModuleContract struct {
	Modules []ModuleContractEntry `json:"modules"`
}

// ModuleContractEntry describes a single registered module and the sorted
// names of the modules it declares as dependencies.
type ModuleContractEntry struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"depends_on"`
}

// ModuleContract returns a deterministic, JSON-marshalable description of the
// registered modules and their declared dependencies. Modules are sorted
// alphabetically by name, and each module's DependsOn list is sorted
// alphabetically as well, so that two calls against the same manager state
// produce byte-identical JSON.
func (m *Manager) ModuleContract() ModuleContract {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]ModuleContractEntry, 0, len(names))
	for _, name := range names {
		mod := m.modules[name]
		deps := append([]string(nil), mod.DependsOn()...)
		sort.Strings(deps)
		if deps == nil {
			deps = []string{}
		}
		entries = append(entries, ModuleContractEntry{Name: name, DependsOn: deps})
	}

	return ModuleContract{Modules: entries}
}

// TaskDiagnostic is a safe, name-only snapshot of a supervised task's
// completion status. It surfaces exactly the information TaskHandle.Wait
// already exposes to callers today (a name, whether the task has finished,
// and its final error, if any) so including it in Diagnostics does not leak
// anything new.
type TaskDiagnostic struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
	Err  string `json:"error,omitempty"`
}

// ModuleTiming records how long a single module took during an InitModules
// or StartModules phase.
type ModuleTiming struct {
	Name       string        `json:"name"`
	DurationNs time.Duration `json:"duration_ns"`
}

// LifecycleTimings captures how long the InitModules and StartModules
// phases took, in total and (when available) per module. Durations are
// time.Duration values, which marshal to JSON as an integer count of
// nanoseconds. All fields are zero-valued/omitted until the corresponding
// phase has run at least once; they are never fabricated.
type LifecycleTimings struct {
	InitModules  time.Duration  `json:"init_modules_ns,omitempty"`
	StartModules time.Duration  `json:"start_modules_ns,omitempty"`
	ModuleInit   []ModuleTiming `json:"module_init,omitempty"`
	ModuleStart  []ModuleTiming `json:"module_start,omitempty"`
}

// Diagnostics is a point-in-time, machine-readable snapshot of the manager's
// internal state: lifecycle state, the module dependency graph, registered
// service names, supervised task status, health/readiness check names, and
// lifecycle timings.
//
// Diagnostics is safe to log, export, or attach to a support ticket: it
// deliberately never includes registered service values (only their sorted
// names), health/readiness check function bodies (only their sorted names),
// task closures, or any other internal implementation detail such as
// mutexes or the internal task cancellation context. Only names, booleans,
// error strings, and durations are exposed.
type Diagnostics struct {
	State           string           `json:"state"`
	Modules         ModuleContract   `json:"modules"`
	Services        []string         `json:"services"`
	Tasks           []TaskDiagnostic `json:"tasks"`
	HealthChecks    []string         `json:"health_checks"`
	ReadinessChecks []string         `json:"readiness_checks"`
	Timings         LifecycleTimings `json:"timings,omitempty"`
}

// Diagnostics returns a snapshot of the manager's current state. See the
// Diagnostics type doc for the safety guarantees this method provides.
func (m *Manager) Diagnostics() Diagnostics {
	return Diagnostics{
		State:           m.State().String(),
		Modules:         m.ModuleContract(),
		Services:        m.serviceNames(),
		Tasks:           m.taskDiagnostics(),
		HealthChecks:    sortedCheckNames(m.HealthChecks()),
		ReadinessChecks: sortedCheckNames(m.ReadinessChecks()),
		Timings:         m.lifecycleTimings(),
	}
}

// serviceNames returns the sorted names of registered services. It
// deliberately never returns the service values themselves.
func (m *Manager) serviceNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.services))
	for name := range m.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// taskDiagnostics returns a sorted snapshot of the currently supervised
// tasks. A task that has already finished no longer appears here: Manager.Go's
// completion goroutine removes a task from m.tasks before it marks the
// TaskHandle done, both under taskMu, so holding taskMu for the whole
// snapshot (rather than just the map copy) guarantees every task returned
// here is still in flight at the instant it was observed — Done is
// therefore always false and Err always empty for entries in this slice.
// The fields are kept on TaskDiagnostic regardless, since they mirror what
// TaskHandle already exposes and keep the type meaningful if the manager
// ever starts retaining finished tasks for a short grace period.
func (m *Manager) taskDiagnostics() []TaskDiagnostic {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()

	diags := make([]TaskDiagnostic, 0, len(m.tasks))
	for _, t := range m.tasks {
		done := t.Done()
		var errStr string
		if done {
			if err := t.Err(); err != nil {
				errStr = err.Error()
			}
		}
		diags = append(diags, TaskDiagnostic{Name: t.Name(), Done: done, Err: errStr})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Name < diags[j].Name })
	return diags
}

// sortedCheckNames converts a name -> check function map (as returned by
// HealthChecks/ReadinessChecks) into a sorted slice of names, discarding the
// function values so diagnostics never expose check bodies.
func sortedCheckNames(checks map[string]func(ctx context.Context) error) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
