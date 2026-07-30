# EventBus Messaging Capabilities Guide

This guide documents MOD-54, the second roadmap item from
[ADR-0031](../adr/adr-0031-modulex-value-and-specialization-roadmap.md): splitting
`modulex.EventBus`'s bundled publish/subscribe surface into explicit,
independently checkable messaging capability interfaces, without changing
`EventBus` itself or any existing adapter's exported API.

## Why `EventBus` stays exactly as it is

`EventBus` is Modulex's most widely-used interface, implemented by every
adapter in this module (`nats`, `rabbitmq`, `watermill`) and the in-package
no-op fallback. Changing its method set would be a breaking change for every
consumer. It is not necessary anyway: Go interfaces are structural, so a new,
narrower interface whose method set is a subset of `EventBus`'s is
automatically satisfied by every existing `EventBus` implementation with zero
code changes on their part.

`EventBus` therefore remains the stable, minimal core interface: `Publish`,
`Subscribe`, `Close`. What changes is that two new interfaces —
`Publisher` and `Subscriber` — name the two halves of that bundle
explicitly, and a third, genuinely new interface — `DurableConsumer` — names
a capability `EventBus` never had and not every adapter provides.

## The new interfaces

### `Publisher`

```go
type Publisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}
```

The narrow capability of publishing a payload to a topic. Makes no promise
about delivery durability beyond what the concrete adapter documents. Every
`EventBus` implementation already satisfies `Publisher`.

### `Subscriber`

```go
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, handler EventHandler) error
}
```

The narrow capability of registering a fire-and-forget handler for a topic.
**Subscriber makes no durability guarantee.** Whether a delivered message is
retried, redelivered, or silently dropped on handler error is entirely
adapter-defined — see each adapter's `Subscribe` doc comment for its specific
policy. A caller that needs at-least-once delivery, explicit acknowledgement,
replay, or dead-letter semantics must use `DurableConsumer` instead; `EventBus`
/ `Subscriber` never imply any of that, by design. Every `EventBus`
implementation already satisfies `Subscriber`.

### `DurableConsumer`

```go
type DurableConsumer interface {
    SubscribeDurable(ctx context.Context, topic string, handler DurableHandler, opts ...DurableSubscribeOption) error
}
```

The capability of consuming a topic with the stronger guarantees `Subscriber`
deliberately does not promise. **Not every adapter implements
`DurableConsumer`** — callers that need these guarantees should type-assert
for it (`if dc, ok := bus.(modulex.DurableConsumer); ok { ... }`) rather than
assuming any `EventBus` or `Subscriber` provides them.

`DurableConsumer` deliberately does not embed `Subscriber`. A durable handler
needs to express more than "error or nil" (see below), so it uses a distinct
handler signature (`DurableHandler`) rather than `EventHandler`; the two
therefore cannot share one `Subscribe` method identity. An adapter is free to
implement both `Subscriber` and `DurableConsumer` (as `nats.JetStreamEventBus`
does, trivially, since its `Subscribe` already exists — see below), but the
capabilities are independent and checked independently.

#### Design choice: a typed `AckDecision`, not a bare `error`

`EventHandler`'s bare `error` return cannot distinguish "please retry this
message" from "give up on this message without retrying" from "this message
can never succeed, route it to a dead letter" — a durable consumer with real
ack/nack/dead-letter semantics needs to express all three. Rather than layer
this through subscription-time configuration alone (the other legitimate
design the interface could have chosen), `DurableHandler` returns an explicit
`AckDecision`:

```go
type AckDecision int

const (
    Ack        AckDecision = iota // acknowledge; will not be redelivered
    Nack                          // redeliver, subject to the adapter's retry policy
    DeadLetter                    // never redeliver; route to the adapter's dead-letter mechanism
)

type DurableMessage struct {
    Payload       []byte
    Redelivered   bool // true if this is a retry
    DeliveryCount int  // 1 for the first delivery; 0 if the adapter can't track it
}

type DurableHandler func(ctx context.Context, msg DurableMessage) AckDecision
```

This keeps the ack decision in the type system and in the handler's control
flow, rather than requiring the caller to infer intent from an `error` value
plus out-of-band configuration.

## The six semantics, and how `DurableConsumer` addresses each

MOD-54 names six semantics a durable consumer must be explicit about. Three
are part of the `SubscribeDurable` method contract; three are documented
properties a correct implementation must uphold rather than callable
operations (ordering, in particular, is a property of *how* an implementation
processes what it fetches, not something a caller invokes).

| Semantic | Where it lives | Summary |
|---|---|---|
| Acknowledgement | `DurableHandler`'s return value | `Ack`/`Nack`/`DeadLetter`, translated by the adapter into its broker's native ack mechanism. |
| Consumer identity | `DurableSubscribeOptions.ConsumerName` | Names a durable consumer/consumer-group. Reusing a name resumes from its last acknowledged position, including across restarts; multiple concurrent subscriptions sharing a name load-balance across them. |
| Replay | `DurableSubscribeOptions.Replay` (`ReplayAll` / `ReplayNew`) | Selects where a **brand-new** `ConsumerName` starts reading from. Has no effect on a pre-existing consumer identity, which always resumes rather than replays. |
| Retry | Documented adapter property | On `Nack`, the adapter redelivers subject to its own retry/backoff/max-attempts policy. Adapter-defined because retry configuration varies too much across brokers to standardize at this interface's level. |
| Ordering | Documented adapter property | Within one `SubscribeDurable` call, an implementation must resolve (ack/nack/dead-letter) each message before fetching the next, so relative order is preserved for that subscription. Not guaranteed across multiple subscriptions sharing one `ConsumerName`. |
| Dead-letter | Documented adapter property | On `DeadLetter`, the adapter must never redeliver the message again; how it is routed instead (separate subject/stream, broker-native DLQ, or discarded) is adapter-defined. |

## Which adapters implement which interfaces

| Adapter type | `Publisher` | `Subscriber` | `DurableConsumer` |
|---|---|---|---|
| `nats.EventBus` | yes (via `EventBus`) | yes (via `EventBus`) | no |
| `nats.JetStreamEventBus` | yes (via `EventBus`) | yes (via `EventBus`; `Subscribe` still returns `ErrJetStreamSubscribeUnsupported` — unchanged from before MOD-54) | **yes (new, MOD-54)** |
| `rabbitmq.EventBus` | yes (via `EventBus`) | yes (via `EventBus`) | no |
| `watermill.EventBus` | yes (via `EventBus`) | yes (via `EventBus`) | no |

`nats.JetStreamEventBus` is MOD-54's proof-of-concept `DurableConsumer`
implementation. It adds `SubscribeDurable` alongside its existing
`Publish`/`Subscribe`/`Close` (all unchanged), using a real JetStream
pull-based durable consumer:

- **Acknowledgement**: `msg.Ack()` / `msg.Nak()` / (for `DeadLetter`) a
  republish to a configurable dead-letter subject followed by `msg.Term()`.
- **Retry**: JetStream redelivers a `Nack`ed message after
  `WithDurableAckWait` (default 30s), up to `WithDurableMaxDeliver` total
  attempts (default 5) — JetStream's own max-deliver behavior, not something
  the adapter enforces separately.
- **Replay**: `modulex.ReplayAll` (default) maps to JetStream's `DeliverAll`
  policy; `modulex.ReplayNew` maps to `DeliverNew`. Only affects a brand-new
  consumer name.
- **Ordering**: one sequential pull loop per `SubscribeDurable` call —
  fetches a batch, resolves every message in it in order, only then fetches
  the next.
- **Consumer identity**: `ConsumerName` becomes the JetStream durable
  consumer name (a competing-consumers group when shared across concurrent
  `SubscribeDurable` calls).
- **Dead-letter**: `DeadLetter` republishes to `topic + suffix` (default
  suffix `.DEAD`, configurable via `WithDurableDeadLetterSuffix`; pass `""`
  to disable republishing and only terminate the original) before calling
  `msg.Term()`. The dead-letter subject must be covered by a JetStream stream
  (same as any JetStream publish) or the republish fails — the original
  delivery is still terminated in that case so it is not redelivered
  forever, and the failure is logged.
- **Panic safety**: a `DurableHandler` invocation that panics is recovered
  and treated exactly like an explicit `Nack` — logged, then redelivered
  subject to the consumer's normal retry policy. Without this, an
  unrecovered panic inside the consume loop's goroutine would crash the
  entire process, not just that one subscription — too high a blast radius
  for a single bad message or a buggy handler path. This mirrors the core
  `Manager`'s own `PanicPolicyLog` default for supervised tasks.

See `nats/jetstream.go` for the full implementation and `nats/jetstream_test.go`
for contract tests covering ack, nack/retry with delivery metadata, an
unrecognized `AckDecision` being treated as `Nack`, dead-letter
termination/republish (and disabling it), consumer-identity resumption across
a simulated restart, both replay policies, context-cancellation shutdown of
the pull loop, and `Close` waiting for an in-flight handler.

MOD-57 ("production-grade durable consumer with full operational
documentation") and MOD-55 (gRPC/Connect adapters) are separate, later
tickets; this guide covers MOD-54's scope only.

## Migration guidance

**Existing code using `EventBus` directly needs zero changes.** `EventBus`'s
method set, and every adapter's exported API, are unchanged by MOD-54 — this
was an explicit, hard constraint (see the "Preserving adapters" note below).
`Publisher` and `Subscriber` are new names for capabilities every adapter
already had; nothing needs to newly implement them.

If you want to narrow a dependency from the full `EventBus` down to just what
you use, you can now write `modulex.Publisher` or `modulex.Subscriber` instead
of `modulex.EventBus` with no other code changes, since every existing
`EventBus` value already satisfies both.

If you want durable, acknowledged consumption today, use
`nats.NewJetStreamEventBus` and type-assert (or, since you constructed it
directly, call `SubscribeDurable` on it directly) — see the code example in
`nats/jetstream_test.go`.

For other breaking-change migrations, see
[`docs/planning/migration-guide.md`](migration-guide.md).

### Preserving adapters

Per the MOD-54 ticket and this repo's [`COMPATIBILITY.md`](../../COMPATIBILITY.md)
v0 policy, this change is purely additive:

- `EventBus`'s `Publish`/`Subscribe`/`Close` signatures are unchanged.
- No existing adapter's exported method signatures changed:
  `nats.EventBus`, `nats.JetStreamEventBus` (`Publish`, `Subscribe`, `Close`,
  `NewJetStreamEventBus`, `JetStreamOption`, `WithJetStreamLogger` all
  unchanged), `rabbitmq.EventBus`, and `watermill.EventBus` all keep their
  current exported surface exactly as-is.
- `make check-api-compat` reports only additions (`Publisher`, `Subscriber`,
  `DurableConsumer`, `AckDecision`, `DurableMessage`, `DurableHandler`,
  `ReplayPolicy`, `DurableSubscribeOptions`, `DurableSubscribeOption`,
  `WithConsumerName`, `WithReplayPolicy`, and the new `nats` package
  `SubscribeDurable` method plus its `With*` construction options), never
  removals or signature changes.
