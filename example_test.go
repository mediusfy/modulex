package modulex_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/mediusfy/modulex"
)

// greetingService is a trivial domain service used in the examples.
type greetingService struct {
	greeting string
}

func (s greetingService) Greet(name string) string {
	return fmt.Sprintf("%s, %s!", s.greeting, name)
}

func ExampleProvide() {
	manager := newExampleManager()

	key := modulex.NewKey[greetingService]("example.GreetingService")
	if err := modulex.Provide(manager, key, greetingService{greeting: "Hello"}); err != nil {
		panic(err)
	}

	svc, err := modulex.Resolve(manager, key)
	if err != nil {
		panic(err)
	}

	fmt.Println(svc.Greet("Modulex"))
	// Output: Hello, Modulex!
}

func ExampleManager_InitModules() {
	manager := newExampleManager()

	mod := &exampleModule{}
	if err := manager.RegisterModule(mod); err != nil {
		panic(err)
	}

	if err := manager.InitModules(context.Background()); err != nil {
		panic(err)
	}

	fmt.Println("initialized")
	// Output: initialized
}

func ExampleManager_StartModules() {
	manager := newExampleManager()

	mod := &exampleModule{}
	if err := manager.RegisterModule(mod); err != nil {
		panic(err)
	}
	if err := manager.InitModules(context.Background()); err != nil {
		panic(err)
	}
	if err := manager.StartModules(context.Background()); err != nil {
		panic(err)
	}

	fmt.Println("started")
	// Output: started
}

func ExampleManager_StopModules() {
	manager := newExampleManager()

	mod := &exampleModule{}
	if err := manager.RegisterModule(mod); err != nil {
		panic(err)
	}
	if err := manager.InitModules(context.Background()); err != nil {
		panic(err)
	}
	if err := manager.StartModules(context.Background()); err != nil {
		panic(err)
	}
	if err := manager.StopModules(context.Background()); err != nil {
		panic(err)
	}

	fmt.Println("stopped")
	// Output: stopped
}

type exampleModule struct{}

func (m *exampleModule) Name() string                                 { return "example" }
func (m *exampleModule) DependsOn() []string                          { return nil }
func (m *exampleModule) Init(context.Context, modulex.Registry) error { return nil }
func (m *exampleModule) Start(context.Context) error                  { return nil }
func (m *exampleModule) Stop(context.Context) error                   { return nil }

func newExampleManager() *modulex.Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return modulex.NewManager(nil, logger, nil)
}
