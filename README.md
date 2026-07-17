# Modulex

[![Go Reference](https://pkg.go.dev/badge/github.com/mediusfy/modulex.svg)](https://pkg.go.dev/github.com/mediusfy/modulex)
[![Go Report Card](https://goreportcard.com/badge/github.com/mediusfy/modulex)](https://goreportcard.com/report/github.com/mediusfy/modulex)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Modulex** is a lightweight, thread-safe, dependency-aware module registry and service locator for building linear, decoupled Go applications. It is specifically designed to facilitate the development of **Modular Monoliths** that can be easily split into **Microservices** (standalone binaries) in the future without changing the core business logic.

---

## Key Features

* **Hexagonal Architecture Isolation:** Decouple features by resolving service boundaries dynamically at runtime using interfaces, eliminating compile-time dependency entanglements.
* **Topological Lifecycle Sort (DAG):** Modules specify their dependencies using `DependsOn() []string`. Modulex constructs a Directed Acyclic Graph (DAG), validates circular dependencies, and boots modules in topological order.
* **Reverse Graceful Shutdown:** Automatically tears down resources and stops active worker threads in the strict **reverse** order of their initialization.
* **NATS Subscription Autoclean:** Wraps and manages NATS subscriptions, guaranteeing automatic message route unsubscription before modules begin stopping.
* **State Locking Protection:** Enforces immutability by locking the registry after boot. Any post-initialization service registration returns an error, preventing concurrency races and runtime configuration drift.

---

## Directory Standards

Every feature using `modulex` is recommended to adhere to a clean **Hexagonal Layout**:

```
internal/features/myfeature/
├── domain/            # Entities, pure values, core business rules (Zero feature imports)
├── ports/             # Contracts: B's inbound port interface & outbound requirements
│   ├── service.go     # Inbound interface (e.g. type Service interface)
│   └── repo.go        # Outbound interface (e.g. type Repository interface)
├── service/           # Implements ports/service.go using domain business logic
└── adapters/          # Inbound handlers (HTTP/NATS) & Outbound engines (DB, API clients)
    ├── http.go        # HTTP endpoints handler
    ├── db_repo.go     # Database repository implementation (Postgres/SQLite)
    └── client.go      # Remote adapter client implementing ports/service.go (for standalone mode)
```

---

## Getting Started

### Installation

```bash
go get github.com/mediusfy/modulex
```

### Usage Example

#### 1. Define a Module

```go
package incident

import (
	"context"
	"github.com/mediusfy/modulex"
)

type Module struct {
	svc Service
}

func (m *Module) Name() string {
	return "incident"
}

func (m *Module) DependsOn() []string {
	// Initializes after database module
	return []string{"database"}
}

func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
	// Resolve database connection
	dbConn, err := reg.ResolveService("database.Connection")
	if err != nil {
		return err
	}

	// Instantiate Hexagonal core
	m.svc = NewService(dbConn)

	// Expose inbound port/service to other modules
	return reg.RegisterService("incident.Service", m.svc)
}

func (m *Module) Start(ctx context.Context) error {
	// Start background workers
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	// Clean up resources
	return nil
}
```

#### 2. Bootstrap the Application (Monolith Mode)

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	
	gochi "github.com/go-chi/chi/v5"
	"github.com/mediusfy/modulex"
)

func main() {
	router := gochi.NewRouter()
	mgr := modulex.NewManager(router, nil, slog.Default(), nil)

	// Register modules
	mgr.RegisterModule(&database.Module{})
	mgr.RegisterModule(&incident.Module{})

	ctx := context.Background()

	// Initialize & start modules in topological order
	if err := mgr.InitModules(ctx); err != nil {
		panic(err)
	}
	if err := mgr.StartModules(ctx); err != nil {
		panic(err)
	}

	// Graceful shutdown logic ...
	defer mgr.StopModules(ctx)
}
```

---

## Topology Agility (Moving to Standalone Binaries)

When you decide to deploy `incident` as a separate microservice:

1. **Monolith Configuration:** Remove `mgr.RegisterModule(&incident.Module{})`. Instead, register a client proxy adapter that implements the `IncidentService` interface:
   ```go
   clientAdapter := incidentclient.NewHTTPClient("http://incident-service:8080")
   mgr.RegisterService("incident.Service", clientAdapter)
   ```
2. **Standalone Runner:** In a separate git folder or binary definition, spin up a lightweight registry containing only `incident.Module`.

Other features in the monolith resolve `"incident.Service"` and execute method calls exactly as they did before, unaware that the call is now transported over the network rather than executed in-process.

---

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
