package chi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	gochi "github.com/go-chi/chi/v5"
	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type chiModule struct {
	name   string
	path   string
	handle func(w http.ResponseWriter, r *http.Request)
}

func (m *chiModule) Name() string        { return m.name }
func (m *chiModule) DependsOn() []string { return nil }

func (m *chiModule) Init(_ context.Context, reg modulex.Registry) error {
	router, err := modulexchi.ResolveRouter(reg)
	if err != nil {
		return err
	}
	router.Get(m.path, m.handle)
	return nil
}

func newTestManager(eb modulex.EventBus) *modulex.Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := modulex.NewManager(modulex.WithEventBus(eb), modulex.WithLogger(logger))
	if err != nil {
		panic(err)
	}
	return mgr
}

func TestChiRouterRegistrationAndResolution(t *testing.T) {
	router := gochi.NewRouter()
	mgr := newTestManager(nil)

	require.NoError(t, modulexchi.RegisterRouter(mgr, router))

	mod := &chiModule{
		name: "hello",
		path: "/hello",
		handle: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello from chi"))
		},
	}
	require.NoError(t, mgr.RegisterModule(mod))
	require.NoError(t, mgr.InitModules(context.Background()))

	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello from chi", rec.Body.String())
}

func TestRouterKeyTypeSafety(t *testing.T) {
	router := gochi.NewRouter()
	mgr := newTestManager(nil)

	require.NoError(t, modulexchi.RegisterRouter(mgr, router))

	resolved, err := modulexchi.ResolveRouter(mgr)
	require.NoError(t, err)
	assert.Same(t, router, resolved)
}

func TestRouterErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, mgr *modulex.Manager)
		action  func(mgr *modulex.Manager) error
		wantErr error
	}{
		{
			name:  "resolve without registration",
			setup: func(t *testing.T, mgr *modulex.Manager) {},
			action: func(mgr *modulex.Manager) error {
				_, err := modulexchi.ResolveRouter(mgr)
				return err
			},
			wantErr: modulex.ErrServiceNotFound,
		},
		{
			name: "register duplicate",
			setup: func(t *testing.T, mgr *modulex.Manager) {
				require.NoError(t, modulexchi.RegisterRouter(mgr, gochi.NewRouter()))
			},
			action: func(mgr *modulex.Manager) error {
				return modulexchi.RegisterRouter(mgr, gochi.NewRouter())
			},
			wantErr: modulex.ErrDuplicateService,
		},
		{
			name: "resolve type mismatch",
			setup: func(t *testing.T, mgr *modulex.Manager) {
				// Register a value under the same string key with the wrong concrete type.
				require.NoError(t, mgr.RegisterService(modulexchi.RouterKey.Name(), "not-a-router"))
			},
			action: func(mgr *modulex.Manager) error {
				_, err := modulexchi.ResolveRouter(mgr)
				return err
			},
			wantErr: modulex.ErrServiceTypeMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(nil)
			tt.setup(t, mgr)
			assert.ErrorIs(t, tt.action(mgr), tt.wantErr)
		})
	}
}
