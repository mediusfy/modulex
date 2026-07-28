# ADR-0031: Modulex Value and Specialization Roadmap

## Status

Proposed

## Context

Modulex provides lifecycle orchestration, optional typed service wiring,
supervised background tasks, health and readiness checks, tracing integration,
and adapters for selected infrastructure technologies. Its central value is
allowing a feature module to run inside a modular monolith or behind a local or
remote implementation without moving business logic into infrastructure code.

The next stage of the project should increase that value without turning the
core package into a general-purpose application framework or flattening the
semantic differences between infrastructure technologies. The project also
needs a concrete adoption path that can be validated against Badger and at
least one independent external consumer.

## Decision

Modulex will evolve as a small, stable lifecycle core surrounded by focused
capability packages and development tools. The roadmap is ordered as follows:

1. Stabilize the core lifecycle and typed-wiring APIs before committing to the
   v1 compatibility promise.
2. Split messaging capabilities so simple publishing does not imply support
   for durable consumption semantics.
3. Add gRPC and Connect support for local-to-remote module topology changes.
4. Add a production-grade durable consumer integration, beginning with Kafka /
   Redpanda or a full JetStream consumer implementation.
5. Provide module scaffolding and an official test harness for the recommended
   domain/ports/service/adapters layout.
6. Add runtime diagnostics and module-contract export for operators,
   architecture reviews, and generated documentation.
7. Validate the complete adoption workflow with Badger and a second external
   consumer application.

### 1. Core stability

The core package remains responsible for lifecycle state, dependency graph
validation, rollback, shutdown, typed service keys, and supervised tasks. New
features must not require consumers to import unrelated broker, router, or
telemetry dependencies. Public API changes will be reviewed against the v1
compatibility policy.

### 2. Messaging capabilities

The current event-bus abstraction is retained for simple publish/subscribe use
cases, but durable messaging will use explicit capability interfaces. The
design must represent acknowledgements, retries, consumer identity, replay,
ordering, and dead-letter behavior where the underlying technology supports
them. An adapter must not claim to provide semantics that its interface cannot
express.

### 3. RPC topology adapters

gRPC and Connect adapters will implement the same port-oriented topology model
as local services. They will cover server lifecycle, client registration,
graceful shutdown, health integration, and consistent error/context
propagation. Transport-specific behavior belongs in optional packages, not in
the lifecycle core.

### 4. Durable consumers

The first durable-consumer implementation will be selected based on adoption
value and semantic fit. Kafka/Redpanda and NATS JetStream are the initial
candidates. The implementation must expose the technology's delivery model
explicitly rather than adapting it into a fire-and-forget callback that hides
acknowledgement or redelivery behavior.

### 5. Scaffolding and testing

Modulex will provide a small scaffolding workflow and a reusable test harness.
Generated modules will use constructor injection by default, keep interfaces in
ports packages, and make lifecycle ownership visible. The test harness will
cover ordering, rollback, cancellation, shutdown deadlines, health/readiness,
and resource ownership.

### 6. Diagnostics and contract export

Modulex will expose a diagnostic snapshot and a machine-readable module
contract. The output should include lifecycle state, dependency edges,
provided/required services, active supervised tasks, health/readiness status,
and relevant lifecycle timings. Export formats may include JSON and Mermaid,
but the contract model must remain independent of presentation.

### 7. Adoption validation

Badger will be the primary adoption case. A second external consumer will prove
that the APIs and boundary checks work outside the repository's own examples.
Validation will exercise both monolithic and remote-adapter composition where
possible, and will run the repository's required tests, race checks, build,
lint, boundary checks, and compatibility checks.

## Forking and extension policy

Technology integrations should be optional packages or sibling repositories by
default. A fork is justified only when a technology has incompatible lifecycle
or delivery semantics, requires an independent compatibility policy, or needs
a substantially different public API and release cadence. Forks must preserve
the core contracts where practical and document which semantics intentionally
diverge.

Potential specializations include gRPC/Connect, Kafka/Redpanda, durable NATS
JetStream, Redis Streams, cloud queues, Kubernetes lifecycle integration, and
workflow engines such as Temporal. These should not expand the core package
unless the capability is technology-neutral and useful to every consumer.

## Non-goals

This roadmap does not make Modulex a service mesh, deployment platform,
compile-time import enforcer, generated dependency-injection framework, or
universal messaging abstraction. Deployment, network policy, compile-time
architecture enforcement, and broker-specific reliability semantics remain
separate concerns.

## Consequences

### Positive

- The core remains small and dependency-light.
- Local-to-remote extraction becomes a supported and testable workflow.
- Messaging adapters can expose reliable technology-specific behavior honestly.
- New teams get a consistent module layout and test strategy.
- Operators and reviewers can inspect the running module topology.
- Badger and external-consumer validation provide evidence of real adoption.

### Negative

- More capability interfaces and packages increase the surface area to
  document and maintain.
- Durable messaging integrations require technology-specific APIs rather than
  one universal abstraction.
- A scaffolding tool and test harness create compatibility obligations.
- Supporting both adapters and independent forks can create ecosystem drift.

## Success criteria

- Core consumers compile without optional adapter dependencies.
- Lifecycle and typed-wiring APIs have an explicit v1 compatibility policy.
- At least one durable consumer has tested acknowledgement, retry, replay, and
  shutdown behavior.
- gRPC or Connect supports swapping a local implementation for a remote client
  through the composition root.
- A generated module passes the official test harness and boundary analyzer.
- Diagnostic and contract exports are usable by humans and automation.
- Badger and a second external consumer pass the documented verification
  workflow.

## Related work

- `docs/planning/library-readiness-checklist.md`
- `docs/planning/comparison-with-alternatives.md`
- `docs/planning/badger-adoption-guide.md`
- Jira MOD-53: Document and validate Modulex adoption path for Badger
