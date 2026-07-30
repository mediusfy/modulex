# ADR-0033: Durable Consumer Integration via NATS JetStream

## Status

Accepted

## Context

ADR-0031's fourth roadmap item (Jira MOD-57, "Add a production-grade durable
consumer integration") calls for selecting a durable messaging technology
(Kafka/Redpanda or NATS JetStream) and implementing real acknowledgement,
retry/redelivery, replay, consumer identity, and dead-letter behavior —
explicitly not reducible to the existing `EventBus.Subscribe`'s
fire-and-forget callback, whose plain `error` return cannot distinguish
"retry this" from "give up" from "this can never succeed."

MOD-54 (ADR-0031 roadmap item 2, "Split EventBus into explicit messaging
capabilities") defined the `Publisher`/`Subscriber`/`DurableConsumer`
capability interfaces this integration needed to target, and — because NATS
JetStream was already reachable from a direct dependency this module already
carries (`github.com/nats-io/nats.go`, used by the plain `nats.EventBus`
adapter) — implemented `DurableConsumer` against `nats.JetStreamEventBus` as
part of that same ticket, to have at least one concrete adapter demonstrating
the new capability model. This ADR records that technology choice formally
and specifies the semantic contract the implementation commits to, per this
ticket's own acceptance criterion ("the chosen technology and semantic
contract are documented in an ADR or design note").

## Decision

**NATS JetStream**, not Kafka/Redpanda, is the durable-consumer technology
this module ships a first-party adapter for.

### Why JetStream over Kafka/Redpanda

- **Zero new dependency.** `nats-io/nats.go` (which includes JetStream
  support) is already a direct dependency of this module via the plain
  `nats.EventBus` adapter. A Kafka or Redpanda client would be a wholly new
  dependency, with its own transitive graph, for a module whose core package
  already goes to some lengths to keep transport dependencies isolated in
  adapter sub-packages (see `scripts/check-consumer-boundary.sh`).
- **Adoption fit.** Modulex already ships a NATS adapter and this repository
  (and its documented consumers, e.g. `rufkis-platform`) already operate NATS
  infrastructure. Adding JetStream durability on top of infrastructure a
  consumer already runs is a materially smaller adoption cost than
  introducing an entirely new broker technology.
- **Semantic fit.** JetStream's pull-consumer model maps directly onto every
  semantic this ADR needs to specify: explicit ack/nack/term, a native
  durable consumer identity with resumable position, configurable
  redelivery/backoff via ack-wait and max-deliver, and a replay policy
  selectable per new consumer. Nothing in the contract below required
  bridging or approximating a Kafka concept that doesn't map cleanly onto
  NATS.

This is a per-adapter technology choice, not a change to the core module: a
future adapter for Kafka/Redpanda (or any other broker) implementing
`modulex.DurableConsumer` remains fully possible and would live in its own
sub-package, exactly like `nats.JetStreamEventBus` does today.

## Semantic contract

`nats.JetStreamEventBus.SubscribeDurable` (implementing
`modulex.DurableConsumer`) commits to the following, for every message it
delivers:

| Semantic | Contract |
|---|---|
| **Acknowledgement** | The `DurableHandler` returns an explicit `modulex.AckDecision` — `Ack`, `Nack`, or `DeadLetter` — for every message. There is no implicit "no decision" path: even a handler panic is recovered and treated as `Nack` (see "Panic safety" below), never silently dropped or silently acked. |
| **Retry / redelivery** | On `Nack` (or a recovered panic), JetStream redelivers the message after `WithDurableAckWait` (default 30s) elapses without an ack, up to `WithDurableMaxDeliver` total attempts (default 5). Beyond the max-deliver count, JetStream stops redelivering the message on its own — a handler that wants a hard dead-letter guarantee after N failures should return `DeadLetter` explicitly once it observes `DurableMessage.DeliveryCount` approaching that configured limit, rather than relying on JetStream's own exhaustion behavior, which does not itself perform the dead-letter republish this adapter provides. |
| **Replay** | `modulex.ReplayPolicy` (`ReplayAll`/`ReplayNew`) selects where a **brand-new** `ConsumerName` starts reading from — `ReplayAll` maps to JetStream's `DeliverAll`, `ReplayNew` to `DeliverNew`. This only affects a consumer identity's first-ever subscription; reusing an existing `ConsumerName` resumes from its last acknowledged position regardless of `ReplayPolicy`. |
| **Ordering** | One sequential pull loop per `SubscribeDurable` call: a batch is fetched, every message in it is resolved (ack/nack/dead-letter) in order, and only then is the next batch fetched. Relative order is preserved within one subscription. Ordering across multiple concurrent subscriptions sharing one `ConsumerName` (a competing-consumers group) is **not** guaranteed, since JetStream may deliver to whichever subscription is next available. |
| **Consumer identity** | `ConsumerName` becomes the JetStream durable consumer name. Reusing the same name — including across process restarts — resumes from its last acknowledged position. Multiple concurrent `SubscribeDurable` calls sharing one `ConsumerName` load-balance messages across them rather than each receiving every message. |
| **Dead-letter** | On `DeadLetter`, the message is republished to `topic + suffix` (default suffix `.DEAD`, configurable via `WithDurableDeadLetterSuffix`; an empty string disables republishing and only terminates the original) and then `msg.Term()` is called so JetStream never redelivers it through the normal retry path. If the dead-letter republish itself fails (e.g. no stream covers that subject), the original delivery is still terminated — a dead-letter routing failure must not cause the message to retry forever — and the failure is logged. |
| **Shutdown / ownership** | See "Shutdown and ownership semantics" below. |
| **Panic safety** | A `DurableHandler` invocation that panics is recovered and treated exactly like an explicit `Nack`: logged, then redelivered per the retry policy above. Without this, an unrecovered panic in the durable consume loop's dedicated goroutine would crash the entire host process, not just that one subscription. This mirrors the core `Manager`'s own `PanicPolicyLog` default for supervised tasks (see `modulex.go`). |

## Shutdown and ownership semantics

- **Ownership**: `JetStreamEventBus` does not own the underlying
  `*nats.Conn`; it is caller-supplied and caller-closed, matching every other
  `EventBus` adapter in this module. `JetStreamEventBus` does own its
  per-subscription pull loops and their JetStream subscriptions.
- **Shutdown**: `Close(ctx)` cancels every active `SubscribeDurable`
  subscription's context and waits (bounded by `ctx`) for all of their
  consume-loop goroutines to finish their current in-flight message before
  returning. A caller that needs a bounded shutdown should pass a `ctx` with
  a deadline; `Close` does not impose its own internal timeout beyond the
  caller's context, matching `Manager.StopModules`'s existing
  context-is-the-timeout convention elsewhere in this module (see
  `Manager.waitForTasks`).
- **Cancellation**: cancelling the `context.Context` passed to
  `SubscribeDurable` stops that subscription's consume loop. Because
  `nats.go`'s `Fetch` cannot be given a cancellable context directly
  alongside a max-wait duration, cancellation latency is bounded by
  `WithDurableFetchWait` (default 5s), not instantaneous; this is documented
  in `durableConsumeLoop`'s doc comment in the source.

## Existing publish-only users remain compatible

`JetStreamEventBus`'s pre-existing exported surface —
`Publish`/`Subscribe`/`Close`/`NewJetStreamEventBus`/`JetStreamOption`/`WithJetStreamLogger`
— is completely unchanged by this integration (verified via
`make check-api-compat` showing only additions for the `nats` package, plus
one accepted, low-risk `JetStreamEventBus: old is comparable, new is not`
finding from adding synchronization fields — see MOD-54's PR #84 for that
finding's full justification). `Subscribe` still always returns
`ErrJetStreamSubscribeUnsupported`; a caller relying on `Publish`-only
behavior needs no code changes at all.

## Consequences

### Positive

- A real, tested durable-consumer path exists using infrastructure this
  module already integrates with, with zero new dependencies.
- The semantic contract above is precise enough that a caller can reason
  about retry/replay/ordering/dead-letter behavior without reading
  `nats/jetstream.go`'s implementation.
- A future Kafka/Redpanda (or other broker) `DurableConsumer` adapter has a
  concrete precedent to follow or diverge from deliberately.

### Negative

- JetStream's own max-deliver exhaustion does not itself dead-letter a
  message; a handler wanting that guarantee must watch `DeliveryCount` and
  decide to `DeadLetter` proactively (documented above as a caller
  responsibility, not a gap this adapter silently papers over).
- Ordering is only guaranteed within one subscription, not across a
  competing-consumers group sharing one `ConsumerName` — a caller needing
  strict global ordering must use a single subscription for that
  `ConsumerName`, forgoing horizontal scale-out for that consumer identity.

## Related work

- [ADR-0031: Modulex value and specialization roadmap](adr-0031-modulex-value-and-specialization-roadmap.md)
- [`docs/planning/eventbus-capabilities-guide.md`](../planning/eventbus-capabilities-guide.md) — the `Publisher`/`Subscriber`/`DurableConsumer` capability model this integration implements
- [`docs/planning/durable-consumer-operations-guide.md`](../planning/durable-consumer-operations-guide.md) — production configuration and operational failure modes
- `nats/jetstream.go`, `nats/jetstream_test.go` — the implementation and its test suite (integration tests covering success, handler failure/nack-redelivery, handler panic, cancellation, and shutdown all already exist here as of MOD-54)
- Jira MOD-54: Split EventBus into explicit messaging capabilities
- Jira MOD-57: Add a production-grade durable consumer integration (this ADR)
