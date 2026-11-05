package modulex

import (
	"fmt"

	"github.com/mediusfy/modulex/apperrors"
)

// ErrConfigTypeMismatch is returned by the config loader installed via
// WithTypedConfig when GetConfig's target is not a pointer to the configured
// type.
const ErrConfigTypeMismatch apperrors.Error = "config target type mismatch"

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
