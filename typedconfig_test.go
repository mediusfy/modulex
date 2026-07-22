package modulex_test

import (
	"errors"
	"testing"

	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type typedConfigTestConfig struct {
	Name  string
	Value int
}

func TestWithTypedConfig(t *testing.T) {
	cfg := typedConfigTestConfig{Name: "svc", Value: 42}

	tests := []struct {
		name    string
		target  interface{}
		wantErr error
		check   func(t *testing.T, target interface{})
	}{
		{
			name:   "matching pointer type is populated",
			target: &typedConfigTestConfig{},
			check: func(t *testing.T, target interface{}) {
				got, ok := target.(*typedConfigTestConfig)
				require.True(t, ok)
				assert.Equal(t, cfg, *got)
			},
		},
		{
			name:    "mismatched target type returns ErrConfigTypeMismatch",
			target:  &struct{ Other string }{},
			wantErr: modulex.ErrConfigTypeMismatch,
		},
		{
			name:    "non-pointer target returns ErrConfigTypeMismatch",
			target:  typedConfigTestConfig{},
			wantErr: modulex.ErrConfigTypeMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(nil)
			modulex.WithTypedConfig(cfg)(mgr)

			err := mgr.GetConfig(tt.target)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr))
				return
			}
			require.NoError(t, err)
			tt.check(t, tt.target)
		})
	}
}
