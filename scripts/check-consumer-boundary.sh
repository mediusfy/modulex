#!/usr/bin/env bash
# check-consumer-boundary.sh verifies that a consumer importing only the core
# github.com/mediusfy/modulex package does not compile in any integration
# adapter (chi, nats, rabbitmq, watermill, otel) as a build dependency.
#
# examples/external-consumer is a standalone Go module (own go.mod, replacing
# github.com/mediusfy/modulex with the local checkout) that imports only the
# core package, so its build graph reflects what a real external consumer
# would pull in.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../examples/external-consumer"

go build -o /dev/null ./...

forbidden='go-chi|nats-io|rabbitmq|streadway|amqp|ThreeDotsLabs|watermill|go\.opentelemetry\.io'
if go list -deps . | grep -E -i "$forbidden"; then
    echo "error: external-consumer example compiled in an integration adapter dependency" >&2
    exit 1
fi

echo "ok: external-consumer builds without pulling in any integration adapter"
