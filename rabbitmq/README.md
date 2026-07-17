# Modulex RabbitMQ EventBus Adapter

This package provides a `modulex.EventBus` implementation backed by
[RabbitMQ](https://www.rabbitmq.com/) via `github.com/rabbitmq/amqp091-go`.

## Usage

```go
import (
    amqp "github.com/rabbitmq/amqp091-go"
    rabbitadapter "github.com/mediusfy/modulex/rabbitmq"
)

conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
if err != nil {
    return err
}
defer conn.Close()

ch, err := conn.Channel()
if err != nil {
    return err
}
defer ch.Close()

eb := rabbitadapter.NewEventBus(ch)
manager := modulex.NewManager(eb, logger, nil)
```

## Behavior

- `Publish` publishes a message to the default exchange using the topic as the
  routing key, which routes to the queue of the same name.
- `Subscribe` starts a consumer on the named queue and invokes the handler for
  each delivered message.
- `Close` cancels the internal consumer goroutines.

## Testing

The adapter tests connect to a live RabbitMQ broker. By default they target
`amqp://guest:guest@localhost:5672/`. Set `RABBITMQ_URL` to use a different
broker. Tests skip gracefully when no broker is available.

```bash
RABBITMQ_URL=amqp://guest:guest@localhost:5672/ go test ./rabbitmq/...
```
