package service_test

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex/examples/scaffolded-sample/adapters"
	"github.com/mediusfy/modulex/examples/scaffolded-sample/service"
)

func TestServiceCreateAndList(t *testing.T) {
	repo := adapters.NewInMemoryRepository()
	svc := service.New(repo)
	ctx := context.Background()

	created, err := svc.CreateScaffoldedSample(ctx, "example")
	if err != nil {
		t.Fatalf("CreateScaffoldedSample: %v", err)
	}
	if created.Name != "example" {
		t.Errorf("CreateScaffoldedSample: Name = %q, want %q", created.Name, "example")
	}

	items, err := svc.ListScaffoldedSamples(ctx)
	if err != nil {
		t.Fatalf("ListScaffoldedSamples: %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Errorf("ListScaffoldedSamples: got %+v, want a single item matching %+v", items, created)
	}
}
