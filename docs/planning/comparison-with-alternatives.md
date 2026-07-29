# Modulex Compared With Plain Injection, Wire, Fx, and Dig

This document is an honest comparison of Modulex with common Go dependency-
management and lifecycle approaches. It is intended to help teams choose the
right tool for their problem rather than to claim that Modulex is the best
choice in every situation.

## Summary

| Approach              | Type                | Lifecycle | Compile-time safety | Best for |
| --------------------- | ------------------- | --------- | ------------------- | -------- |
| Plain constructor injection | Manual            | None      | Yes (via Go types)  | Small services, libraries, teams that value simplicity |
| Google Wire           | Code generation     | None      | Yes                 | Medium/large applications that want compile-time graphs |
| Uber Dig              | Reflection container| None      | No                  | Teams that prefer a runtime DI container |
| Uber Fx               | Framework on Dig    | Yes       | No                  | Long-running services that need lifecycle + DI |
| Modulex               | Lifecycle manager   | Yes       | Partial             | Modular applications that need deterministic startup/shutdown and optional typed wiring |

## Plain constructor injection

```go
func main() {
    db := NewDB(cfg)
    repo := NewRepository(db)
    svc := NewService(repo)
    srv := NewServer(svc)
    srv.Run()
}
```

### Strengths

- Zero dependencies.
- Maximum compile-time safety: impossible to forget a dependency or wire the
  wrong type.
- Easy to read and debug.
- Works well with Go's explicit error handling.

### Weaknesses

- Manual ordering can become tedious as the graph grows.
- No built-in lifecycle hooks (init, start, stop).
- No standard way to stop services in reverse order on shutdown.

### When to use

For most Go programs, especially libraries and small services, plain
constructor injection is the right default. Modulex does not replace it; it
adds lifecycle orchestration on top of it.

## Google Wire

Wire generates dependency wiring code from a provider graph.

### Strengths

- Compile-time dependency graph validation.
- No runtime reflection.
- Can generate the same code a human would write.

### Weaknesses

- Requires learning provider/injector semantics.
- No built-in lifecycle management.
- Generated code can be noisy in reviews.

### When to use

When you have a large, stable dependency graph and want to guarantee at compile
time that everything is wired correctly. Wire focuses on construction; Modulex
focuses on lifecycle. They can complement each other.

## Uber Dig

Dig is a reflection-based dependency injection container.

### Strengths

- Minimal boilerplate once providers are registered.
- Supports named and optional dependencies.

### Weaknesses

- Runtime errors for missing or ambiguous dependencies.
- Uses reflection, which can make debugging harder.
- No lifecycle management on its own.

### When to use

When your team prefers a runtime DI container and accepts the trade-off of
runtime validation. Modulex deliberately avoids a reflection-based container;
its `Registry` is an optional, typed service locator, not the primary wiring
mechanism.

## Uber Fx

Fx builds on Dig and adds lifecycle hooks, startup, and shutdown.

### Strengths

- Mature, widely used framework.
- Lifecycle hooks with start/stop ordering.
- Rich ecosystem and documentation.

### Weaknesses

- Heavy dependency tree.
- Reflection-based wiring inherits Dig's runtime error trade-offs.
- Opinionated about application structure.

### When to use

For large services that already fit Fx's model and where the team is comfortable
with its opinions. Modulex is a smaller, less opinionated alternative for teams
that want deterministic lifecycle ordering without a full framework.

## Modulex

Modulex is a lifecycle and composition library. It does not enforce a directory
structure or claim to prevent unwanted imports at compile time.

### Strengths

- Deterministic init/start/stop ordering based on declared dependencies.
- Reverse-order rollback on failure and reverse-order shutdown.
- Optional typed service keys for safe service location.
- Supervised background tasks with panic recovery and lifecycle-owned
cancellation.
- Small, focused API with minimal mandatory dependencies.

### Weaknesses

- Dependency validation is runtime, not compile-time.
- Does not generate wiring code.
- Does not enforce architectural boundaries; teams must rely on conventions,
  code review, or an external analyzer.
- Some framework integrations (Chi, OpenTelemetry) are still part of the core
  package as of v0 and will be extracted before v1.

### When to use

When you want explicit lifecycle orchestration for modular applications and
prefer constructor injection as the default wiring style, with a typed registry
as an optional escape hatch.

## Migration notes

- From **plain injection**: keep your constructors; wrap the top-level assembly
  in a `Manager` and register modules that implement `Init`/`Start`/`Stop` only
  when lifecycle hooks are needed.
- From **Wire**: Wire can still generate construction code; Modulex can own the
  lifecycle around the generated graph.
- From **Fx/Dig**: Modulex replaces the lifecycle and DI container with smaller,
  explicit modules. Expect to write more constructors and less reflection-based
  registration.
