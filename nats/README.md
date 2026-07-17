# Modulex NATS EventBus Adapter

This package provides a `modulex.EventBus` implementation backed by
[NATS](https://nats.io/).

## Usage

```go
import (
    "github.com/nats-io/nats.go"
    natsadapter "github.com/mediusfy/modulex/nats"
)

conn, err := nats.Connect(nats.DefaultURL)
if err != nil {
    return err
}
defer conn.Close()

eb := natsadapter.NewEventBus(conn)
manager := modulex.NewManager(router, eb, logger, nil)
```

## Behavior

- `Publish` maps directly to `conn.Publish(topic, payload)`.
- `Subscribe` creates a NATS subscription and adapts incoming messages to the
  generic `modulex.EventHandler` signature.
- `Close` unsubscribes all registered subscriptions.

## Testing

The adapter tests start an embedded NATS server using
`github.com/nats-io/nats-server/v2/test`. Run them with:

```bash
go test ./nats/...
```
