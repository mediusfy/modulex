package modulex_test

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type typedSvc struct {
	value string
}

type typedSvcValuer interface {
	Value() string
}

func (s *typedSvc) Value() string { return s.value }

func TestTypedServiceWiring(t *testing.T) {
	tests := []struct {
		name   string
		act    func(t *testing.T, manager *modulex.Manager) error
		assert func(t *testing.T, manager *modulex.Manager, err error)
	}{
		{
			name: "Provide and Resolve round-trip",
			act: func(t *testing.T, manager *modulex.Manager) error {
				key := modulex.NewKey[*typedSvc]("typed.Service")
				return modulex.Provide(manager, key, &typedSvc{value: "hello"})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.NoError(t, err)
				key := modulex.NewKey[*typedSvc]("typed.Service")
				svc, err := modulex.Resolve(manager, key)
				require.NoError(t, err)
				assert.Equal(t, "hello", svc.Value())
			},
		},
		{
			name: "Resolve missing service returns not found",
			act: func(t *testing.T, manager *modulex.Manager) error {
				_, err := modulex.Resolve(manager, modulex.NewKey[*typedSvc]("missing.Service"))
				return err
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrServiceNotFound)
			},
		},
		{
			name: "Resolve with wrong type returns type mismatch",
			act: func(t *testing.T, manager *modulex.Manager) error {
				if err := manager.RegisterService("wrongtype.Service", &typedSvc{value: "x"}); err != nil {
					return err
				}
				_, err := modulex.Resolve(manager, modulex.NewKey[string]("wrongtype.Service"))
				return err
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrServiceTypeMismatch)
				assert.ErrorContains(t, err, "wrongtype.Service")
			},
		},
		{
			name: "Provide empty key is rejected",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return modulex.Provide(manager, modulex.NewKey[*typedSvc]("   "), &typedSvc{})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrInvalidServiceName)
			},
		},
		{
			name: "Resolve empty key is rejected",
			act: func(t *testing.T, manager *modulex.Manager) error {
				_, err := modulex.Resolve(manager, modulex.NewKey[*typedSvc](""))
				return err
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrInvalidServiceName)
			},
		},
		{
			name: "Provide duplicate key is rejected",
			act: func(t *testing.T, manager *modulex.Manager) error {
				key := modulex.NewKey[*typedSvc]("dup.Service")
				require.NoError(t, modulex.Provide(manager, key, &typedSvc{}))
				return modulex.Provide(manager, key, &typedSvc{})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrDuplicateService)
			},
		},
		{
			name: "Provide and Resolve interface type",
			act: func(t *testing.T, manager *modulex.Manager) error {
				key := modulex.NewKey[typedSvcValuer]("interface.Service")
				return modulex.Provide(manager, key, typedSvcValuer(&typedSvc{value: "iface"}))
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.NoError(t, err)
				key := modulex.NewKey[typedSvcValuer]("interface.Service")
				svc, err := modulex.Resolve(manager, key)
				require.NoError(t, err)
				assert.Equal(t, "iface", svc.Value())
			},
		},
		{
			name: "Provide with explicit type argument allows implicit interface conversion",
			act: func(t *testing.T, manager *modulex.Manager) error {
				key := modulex.NewKey[typedSvcValuer]("implicit.Service")
				return modulex.Provide[typedSvcValuer](manager, key, &typedSvc{value: "implicit"})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.NoError(t, err)
				svc, err := modulex.Resolve(manager, modulex.NewKey[typedSvcValuer]("implicit.Service"))
				require.NoError(t, err)
				assert.Equal(t, "implicit", svc.Value())
			},
		},
		{
			name: "Resolve can retrieve services registered via legacy RegisterService",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.RegisterService("legacy.Service", &typedSvc{value: "legacy"})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.NoError(t, err)
				svc, err := modulex.Resolve(manager, modulex.NewKey[*typedSvc]("legacy.Service"))
				require.NoError(t, err)
				assert.Equal(t, "legacy", svc.Value())
			},
		},
		{
			name: "Provide is rejected after registry is locked",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := mocks.NewMockModule(t)
				mod.On("Name").Return("mod-a").Maybe()
				mod.On("DependsOn").Return([]string{}).Maybe()
				mod.On("Init", mock.Anything, mock.Anything).Return(nil).Maybe()
				mod.On("Start", mock.Anything).Return(nil).Maybe()
				mod.On("Stop", mock.Anything).Return(nil).Maybe()
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				return modulex.Provide(manager, modulex.NewKey[*typedSvc]("late.Service"), &typedSvc{})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.ErrorIs(t, err, modulex.ErrRegistryLocked)
			},
		},
		{
			name: "whitespace in key name is trimmed",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return modulex.Provide(manager, modulex.NewKey[*typedSvc]("  trimmed.Service  "), &typedSvc{value: "trimmed"})
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.NoError(t, err)
				// Resolve using the trimmed name directly.
				svc, err := modulex.Resolve(manager, modulex.NewKey[*typedSvc]("trimmed.Service"))
				require.NoError(t, err)
				assert.Equal(t, "trimmed", svc.Value())
				// Resolve using whitespace also works because NewKey trims.
				svc2, err := modulex.Resolve(manager, modulex.NewKey[*typedSvc]("  trimmed.Service  "))
				require.NoError(t, err)
				assert.Equal(t, "trimmed", svc2.Value())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			err := tt.act(t, manager)
			tt.assert(t, manager, err)
		})
	}
}
