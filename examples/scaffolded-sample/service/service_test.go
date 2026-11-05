package service_test

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex/examples/scaffolded-sample/adapters"
	"github.com/mediusfy/modulex/examples/scaffolded-sample/service"
)

func TestServiceCreateAndList(t *testing.T) {
	tests := []struct {
		name  string
		names []string
	}{
		{name: "single item", names: []string{"example"}},
		{name: "multiple items", names: []string{"first", "second"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := service.New(adapters.NewInMemoryRepository())
			ctx := context.Background()

			createdIDs := make(map[string]string, len(tt.names))
			for _, name := range tt.names {
				created, err := svc.CreateScaffoldedSample(ctx, name)
				if err != nil {
					t.Fatalf("CreateScaffoldedSample(%q): %v", name, err)
				}
				if created.Name != name {
					t.Errorf("CreateScaffoldedSample(%q): Name = %q, want %q", name, created.Name, name)
				}
				createdIDs[created.ID] = name
			}

			items, err := svc.ListScaffoldedSamples(ctx)
			if err != nil {
				t.Fatalf("ListScaffoldedSamples: %v", err)
			}
			if len(items) != len(tt.names) {
				t.Fatalf("ListScaffoldedSamples: got %d items (%+v), want %d", len(items), items, len(tt.names))
			}
			for _, item := range items {
				if name, ok := createdIDs[item.ID]; !ok || item.Name != name {
					t.Errorf("ListScaffoldedSamples: unexpected item %+v, want one of %v", item, createdIDs)
				}
			}
		})
	}
}
