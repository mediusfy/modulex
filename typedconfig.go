package modulex

import (
	"errors"
	"fmt"
)

// ErrConfigTypeMismatch is returned by the config loader installed via
// WithTypedConfig when GetConfig's target is not a pointer to the configured
// type.
var ErrConfigTypeMismatch = errors.New("config target type mismatch")

// Example:
//
//	modulex.NewManager(modulex.WithTypedConfig(cfg))
func WithTypedConfig[T any](cfg T) ManagerOption {
	return WithConfigLoader(func(target interface{}) error {
		out, ok := target.(*T)
		if !ok || out == nil {
			return fmt.Errorf("%w: GetConfig target must be *%T, got %T", ErrConfigTypeMismatch, cfg, target)
		}
		*out = cfg
		return nil
	})
}
