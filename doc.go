// Package modulex provides deterministic lifecycle orchestration for modular Go
// applications.
//
// A Manager coordinates feature modules through an explicit lifecycle
// (configuring → initializing → initialized → starting → running → stopping →
// stopped), validates the dependency graph, rolls back partial failures, and
// supervises background tasks. Modules receive a Registry they can use to
// register services, access the router, tracer, logger, event bus, config, and
// start lifecycle-owned tasks.
//
// Typed service registration helpers (Key, Provide, Resolve) are also provided.
//
// EventBus adapter sub-packages such as github.com/mediusfy/modulex/nats,
// github.com/mediusfy/modulex/rabbitmq, and github.com/mediusfy/modulex/watermill
// keep framework dependencies out of the core package.
package modulex
