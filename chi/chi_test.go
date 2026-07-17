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
)

type chiModule struct {
	name   string
	path   string
	handle func(w http.ResponseWriter, r *http.Request)
}

func (m *chiModule) Name() string                { return m.name }
func (m *chiModule) DependsOn() []string         { return nil }
func (m *chiModule) Start(context.Context) error { return nil }
func (m *chiModule) Stop(context.Context) error  { return nil }

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
	return modulex.NewManager(eb, logger, nil)
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

func TestResolveRouterWithoutRegistration(t *testing.T) {
	mgr := newTestManager(nil)
	_, err := modulexchi.ResolveRouter(mgr)
	assert.ErrorIs(t, err, modulex.ErrServiceNotFound)
}

func TestRouterKeyTypeSafety(t *testing.T) {
	router := gochi.NewRouter()
	mgr := newTestManager(nil)

	require.NoError(t, modulexchi.RegisterRouter(mgr, router))

	resolved, err := modulexchi.ResolveRouter(mgr)
	require.NoError(t, err)
	assert.Same(t, router, resolved)
}
