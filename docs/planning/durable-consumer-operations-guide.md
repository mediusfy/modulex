# Durable Consumer Operations Guide

This guide documents production configuration and operational failure modes
for `nats.JetStreamEventBus.SubscribeDurable`, the `modulex.DurableConsumer`
implementation shipped in MOD-54 and formally specified in
[ADR-0033](../adr/adr-0033-durable-consumer-jetstream.md). It complements
that ADR's semantic contract with the practical configuration and
failure-mode guidance an operator needs before running this in production —
the "production configuration and operational failure modes are documented"
acceptance criterion for Jira MOD-57.

If you haven't read [ADR-0033](../adr/adr-0033-durable-consumer-jetstream.md)
or the [EventBus capabilities guide](./eventbus-capabilities-guide.md) yet,
start there for the semantic contract and interface design this guide
assumes.

## Configuration knobs and how to tune them

All five are `JetStreamOption`s passed to `nats.NewJetStreamEventBus`;
defaults are defined in `nats/jetstream.go`.

| Option | Default | What it trades off |
|---|---|---|
| `WithDurableAckWait(d)` | 30s | How long JetStream waits for an ack/nack/term before treating a delivery as abandoned and eligible for redelivery. **Too short** relative to your handler's actual processing time causes spurious redelivery of messages that were still being processed — plan for your handler's p99 latency plus margin, not its average. **Too long** delays recovery when a consumer process dies mid-processing (the message sits "in flight" from JetStream's perspective until ack-wait expires). |
| `WithDurableMaxDeliver(n)` | 5 | Total delivery attempts (including the first) before JetStream stops redelivering on its own. This is a safety net, not your dead-letter mechanism — see "Dead-lettering is a handler decision, not automatic" below. Set it high enough that a handler has a real chance to `DeadLetter` proactively once `DeliveryCount` approaches this limit, not so high that a permanently-failing message churns redeliveries for a long time before anyone acts. |
| `WithDurableBatchSize(n)` | 10 | Messages fetched per pull request. Larger batches reduce the number of pull round-trips (better throughput for many small, fast messages) at the cost of more messages held client-side awaiting sequential processing (worse latency for the tail of a batch if handling is slow) — see "Ordering" in the ADR: a batch is fully resolved in order before the next fetch. |
| `WithDurableFetchWait(d)` | 5s | Maximum time one pull request blocks waiting for at least one message. This also bounds `SubscribeDurable`'s cancellation latency (see ADR-0033's "Cancellation" section) — a shorter fetch wait means faster shutdown response but more frequent empty-batch round-trips under low traffic. |
| `WithDurableDeadLetterSuffix(s)` | `.DEAD` | Subject suffix for dead-lettered messages. Pass `""` to disable republishing entirely (the original delivery is still terminated; nothing is republished anywhere) if you'd rather handle terminal failures entirely inside your own handler logic (e.g. writing to an external system) than via a second JetStream subject. |

### A starting point, not a universal default

The shipped defaults (30s ack-wait, 5 max-deliver, batch of 10, 5s fetch
wait) are reasonable for a moderate-latency handler processing a
moderate-volume stream. They are not tuned for every workload:

- A handler doing synchronous, slow I/O (e.g. calling a downstream HTTP API
  with its own retries) should raise `WithDurableAckWait` well above that
  call's worst-case latency, or messages will be redelivered while still
  being processed by the first attempt.
- A very high-throughput, low-latency handler may want a larger
  `WithDurableBatchSize` to reduce pull overhead.
- A consumer that must react to cancellation quickly (e.g. during frequent
  deploys) should lower `WithDurableFetchWait`, accepting more idle-poll
  round-trips in exchange for faster shutdown.

## Operational failure modes

### Broker unreachable / connection lost

`JetStreamEventBus` does not own the underlying `*nats.Conn` (see ADR-0033's
"Shutdown and ownership semantics") — connection-level reconnection is the
caller's responsibility via `nats.go`'s own connection options (e.g.
`nats.ReconnectWait`, `nats.MaxReconnects`), configured when the caller
constructs the `*nats.Conn` passed (indirectly, via `JetStreamContext`) into
`NewJetStreamEventBus`. During an outage, `SubscribeDurable`'s pull loop's
`Fetch` calls will return errors (logged at `ERROR` level with the topic and
consumer name — see `durableConsumeLoop`); the loop keeps retrying rather
than exiting, so a `Fetch` failure due to a transient broker blip does not
tear down the subscription. Once the underlying connection recovers,
`Fetch` succeeds again and consumption resumes automatically — no manual
`SubscribeDurable` call is needed after a transient outage, as long as the
caller's `*nats.Conn` itself reconnects.

### Ack-wait exceeded (redelivery under load)

If a handler's processing time regularly exceeds `WithDurableAckWait`,
you will see the same message delivered more than once even though the
first attempt eventually succeeds — `DurableMessage.Redelivered` and
`DeliveryCount` on the second delivery will reflect this. This is not a bug
in the adapter; it means `WithDurableAckWait` is misconfigured relative to
actual handler latency. A handler must be safe to invoke more than once for
the same logical message (idempotent, or tolerant of duplicate side
effects) regardless of ack-wait tuning — at-least-once delivery, which is
what this adapter provides, never guarantees exactly-once execution.

### Max-deliver exhausted

**Dead-lettering is a handler decision, not automatic.** JetStream stops
redelivering a message once `WithDurableMaxDeliver` attempts are exhausted,
but this adapter does not itself dead-letter that message at that point —
if the handler never returned `DeadLetter`, the message is simply no longer
redelivered, with no dead-letter copy made. A handler that wants a hard
dead-letter guarantee should inspect `DurableMessage.DeliveryCount` and
return `DeadLetter` explicitly once it's at or near `WithDurableMaxDeliver`
(e.g. `if msg.DeliveryCount >= maxDeliver { return modulex.DeadLetter }`),
rather than relying on JetStream's own exhaustion behavior to route it
anywhere.

### Handler panics

A panicking `DurableHandler` invocation is recovered and treated as `Nack`
(logged at `ERROR` with the panic value, topic, and consumer name) — it does
not crash the process, and the message is redelivered per the normal
retry policy. See ADR-0033's "Panic safety" row for the full contract. A
handler that panics on every delivery of a particular message will
therefore consume redelivery attempts exactly like a handler that
explicitly `Nack`s — plan for that when tuning `WithDurableMaxDeliver` if a
class of malformed messages is expected to reliably crash your handler
logic.

### Shutdown mid-processing

`Close(ctx)` cancels every active subscription and waits (bounded by `ctx`)
for their consume loops to finish the message they're currently processing.
A message whose handler was still running when `Close` was called is
allowed to complete normally (ack/nack/dead-letter as usual) before the
loop exits, as long as `ctx` doesn't expire first — pass a `ctx` with
enough deadline margin for your handler's expected worst-case latency if
you want in-flight messages to finish cleanly during a graceful shutdown,
rather than being abandoned (unacknowledged, and therefore redelivered
later) when `ctx` expires.

### Monitoring recommendations

At minimum, alert on:

- The `ERROR`-level log lines this adapter emits (`durable fetch error`,
  `failed to ack durable message`, `failed to nack durable message`,
  `durable handler panicked, nacking for retry`, `failed to publish
  dead-lettered message`) — each names the topic and consumer name.
- JetStream's own consumer-level metrics (available via the broker's
  monitoring API / `nats` CLI) for a durable consumer's pending/redelivered
  message counts, which reveal a stuck or slow consumer before your
  application-level logs necessarily would.
- The dead-letter subject's message rate, if `WithDurableDeadLetterSuffix`
  is enabled — a rising rate of dead-lettered messages usually indicates a
  systemic handler bug or a bad upstream data shape, not isolated
  transient failures.

## Related work

- [ADR-0033: Durable Consumer Integration via NATS JetStream](../adr/adr-0033-durable-consumer-jetstream.md) — the technology choice and semantic contract this guide operationalizes
- [EventBus Capabilities Guide](./eventbus-capabilities-guide.md) — the `Publisher`/`Subscriber`/`DurableConsumer` interface design
- `nats/jetstream.go`, `nats/jetstream_test.go` — implementation and integration tests (success, nack/redelivery, panic recovery, dead-letter, consumer-identity resumption, replay policy, cancellation, and shutdown are all covered)
- Jira MOD-57: Add a production-grade durable consumer integration
