package incident

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/adapters"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/ports"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/service"
)

// Module acts as the composition root for the incident business feature.
type Module struct {
	svc ports.Service
	reg modulex.Registry
}

// NewModule instantiates the module.
func NewModule() *Module {
	return &Module{}
}

func (m *Module) Name() string {
	return "incident"
}

func (m *Module) DependsOn() []string {
	return nil
}

// Init wires repositories, services, registers service locator ports, and routes.
func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
	m.reg = reg

	repo := adapters.NewInMemoryRepository()
	m.svc = service.New(repo)

	// Register core service for dynamic injection / locator resolution by other features
	if err := modulex.Provide(reg, ports.ServiceKey, m.svc); err != nil {
		return err
	}

	// Subscribe to incident.created events via the pluggable EventBus
	if reg.EventBus() != nil {
		_ = reg.EventBus().Subscribe(ctx, "incident.created", func(ctx context.Context, payload []byte) error {
			reg.Logger().Info("EventBus: received incident created notification", slog.String("payload", string(payload)))
			return nil
		})
	}

	// Mount HTTP handlers via the Chi Router registered as a typed service.
	router, err := modulexchi.ResolveRouter(reg)
	if err != nil {
		return err
	}

	router.Get("/api/incidents", func(w http.ResponseWriter, r *http.Request) {
		list, err := m.svc.ListIncidents(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})

	router.Post("/api/incidents", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title    string `json:"title"`
			Severity string `json:"severity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		inc, err := m.svc.CreateIncident(r.Context(), body.Title, body.Severity)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Publish the incident created event to the abstract EventBus
		if reg.EventBus() != nil {
			eventPayload, _ := json.Marshal(inc)
			_ = reg.EventBus().Publish(r.Context(), "incident.created", eventPayload)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(inc)
	})

	return nil
}

func (m *Module) Start(ctx context.Context) error {
	if m.reg == nil {
		return nil
	}

	m.reg.Logger().Info("incident module background worker started")

	_, err := m.reg.Go(ctx, "incident.heartbeat", func(bgCtx context.Context) error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return nil
			case <-ticker.C:
				m.reg.Logger().Info("incident module heartbeat")
			}
		}
	})
	return err
}

func (m *Module) Stop(ctx context.Context) error {
	return nil
}
