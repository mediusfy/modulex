# ADR-0034: Bounded Message Processing and Optional Worker Pools

## Status

Proposed

## Context

Modulex provides messaging adapters for NATS, RabbitMQ, Watermill, and NATS
JetStream. The adapters currently invoke handlers directly from their consume
loops or broker callbacks. Watermill and JetStream process one message at a
time per subscription, while RabbitMQ and JetStream also tie acknowledgement
or redelivery decisions to handler completion.

This is a safe default, but it limits throughput for workloads containing
independent messages or expensive JSON decoding and business processing. It
also leaves each adapter to evolve its own concurrency, backpressure, and
shutdown behavior. Introducing a goroutine pool without a common contract
could acknowledge messages too early, break ordering, create unbounded queues,
or hide failures and redeliveries.

The project should improve throughput without turning the lifecycle core into a
general-purpose job framework or making `ants` a mandatory dependency for all
consumers. JSON serialization itself is not assumed to be the bottleneck;
benchmarks must demonstrate a benefit before adding pooling or allocation
optimizations.

## Decision

Modulex will introduce a technology-neutral, bounded message-processing
capability as an optional package. The capability will be separate from the
core lifecycle manager and from the basic `EventBus` interface.

The processor contract will make the following behavior explicit:

- maximum in-flight work and queue capacity;
- submission and backpressure behavior when capacity is exhausted;
- context cancellation and bounded shutdown;
- panic recovery and error reporting;
- active-work, queue-depth, rejection, failure, and latency metrics;
- whether completion order is required or independent processing is allowed.

The default adapter behavior remains unchanged: one handler invocation at a
time per subscription, with existing ordering, acknowledgement, retry, and
dead-letter semantics preserved.

Concurrent processing is opt-in. It may be enabled only where the caller
accepts the associated ordering and delivery trade-offs. Ordered workloads may
use keyed or partitioned processing so messages for the same key remain
serial while unrelated keys run concurrently.

`ants/v2` may be used as an implementation adapter behind the processor
capability, but it will not be imported by the lifecycle core. A small
standard-library implementation should remain possible, and benchmark results
must justify selecting `ants` over a bounded channel and fixed worker set.

Adapter integration must follow these rules:

1. Watermill acknowledges only after the submitted handler completes.
2. RabbitMQ acknowledges or rejects only after processing completes, and its
   broker prefetch must bound deliveries consistently with processor capacity.
3. JetStream must not acknowledge before processing completes. Concurrent mode
   must explicitly document relaxed ordering and configure `AckWait` for the
   expected processing latency.
4. Core NATS has no broker acknowledgement or retry semantics; concurrency
   must therefore use bounded submission and expose handler failures through
   logging and metrics.
5. Shutdown must stop accepting new work, cancel or drain according to the
   configured policy, wait for accepted work up to the caller's deadline, and
   release the processor exactly once.

JSON handling will decode a payload once inside the processing task. Payload
size limits and validation occur before queueing where possible. `sync.Pool`,
extra marshaling, and parallel diagnostics generation are explicitly deferred
until profiling or benchmarks show allocation or CPU pressure.

## Alternatives considered

### Add `ants` directly to every adapter

Rejected. This increases mandatory dependency surface, duplicates delivery
semantics across adapters, and makes it easy to acknowledge or retry at the
wrong point in the processing lifecycle.

### Spawn one goroutine per message

Rejected as the default. It may be adequate for low-volume workloads, but it
does not provide bounded in-flight work, queue pressure, or a predictable
shutdown budget.

### Use a single global pool owned by `Manager`

Rejected. A global pool allows unrelated subscriptions or tenants to compete
for capacity and makes broker-specific acknowledgement and ordering behavior
harder to reason about. Pools should normally be owned per subscription or
explicit processing domain.

### Parallelize JSON diagnostics generation

Rejected for now. Diagnostics are snapshots intended to be deterministic and
safe; parallelizing sorting and marshaling adds complexity without evidence
that it is a meaningful bottleneck.

## Consequences

### Positive

- Independent message workloads can use bounded parallelism.
- Backpressure and shutdown become explicit and testable.
- Existing consumers retain current semantics unless they opt in.
- Core consumers do not inherit a worker-pool dependency.
- Metrics make queue pressure, latency, and failures visible.
- JSON processing can be optimized based on measured workload rather than
  assumption.

### Negative

- The repository gains another capability package and configuration surface.
- Concurrent delivery requires careful documentation of ordering and retries.
- Adapter-specific tests become more extensive.
- A pool can increase contention and latency when configured above the useful
  parallelism of the workload.

## Implementation plan

1. Add benchmarks for JSON decode/encode, handler latency, throughput, memory,
   and each adapter's current delivery path.
2. Define the bounded processor interface and a small standard-library
   implementation with deterministic shutdown and panic recovery.
3. Add opt-in integration to Watermill and RabbitMQ, including backpressure,
   acknowledgement, shutdown, and race tests.
4. Add an explicitly concurrent JetStream mode without changing the ordered
   default.
5. Add an optional `ants/v2` adapter only if benchmark results demonstrate a
   material benefit in the target workload.
6. Expose processor diagnostics and metrics without including payload contents
   or secrets.

## Acceptance criteria

- Existing default adapter behavior and ordering guarantees remain unchanged.
- No message is acknowledged before its handler has completed.
- Queue capacity, maximum in-flight work, rejection behavior, and shutdown
  deadlines are covered by tests.
- Panic, cancellation, handler error, redelivery, and dead-letter behavior are
  tested for each affected adapter.
- Benchmarks compare the current implementation, bounded standard-library
  processing, and `ants` where applicable.
- JSON performance changes are justified by benchmark or profile evidence.
- `make test-arch`, `make build`, `make lint`, and `make test` pass.

## Related decisions and work

- [ADR-0031: Modulex Value and Specialization Roadmap](adr-0031-modulex-value-and-specialization-roadmap.md)
- [ADR-0033: Durable Consumer JetStream](adr-0033-durable-consumer-jetstream.md)
- Jira MOD-54: Split EventBus into explicit messaging capabilities
- Jira MOD-35: Implement NATS JetStream durable consumer
