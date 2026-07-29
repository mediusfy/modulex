package modulex

import (
	"fmt"
	"strings"
)

// Key is a typed service locator key. It couples a string identifier with a
// compile-time type so that Provide and Resolve can be type-safe.
type Key[T any] struct {
	name string
}

// NewKey creates a typed service key. Leading and trailing whitespace is trimmed.
func NewKey[T any](name string) Key[T] {
	return Key[T]{name: strings.TrimSpace(name)}
}

func (k Key[T]) Name() string { return k.name }

// Provide registers a typed service implementation in the registry.
// It is a type-safe wrapper around Registry.RegisterService.
func Provide[T any](reg Registry, key Key[T], svc T) error {
	if key.name == "" {
		return fmt.Errorf("%w: service key must not be empty", ErrInvalidServiceName)
	}
	if err := reg.RegisterService(key.name, svc); err != nil {
		return err
	}
	return nil
}

// Resolve retrieves a typed service from the registry.
// It returns ErrServiceNotFound if the key is missing, ErrServiceTypeMismatch
// if the registered value cannot be asserted to T, or ErrInvalidServiceName
// if the key is empty.
func Resolve[T any](reg Registry, key Key[T]) (T, error) {
	var zero T
	if key.name == "" {
		return zero, fmt.Errorf("%w: service key must not be empty", ErrInvalidServiceName)
	}

	raw, err := reg.ResolveService(key.name)
	if err != nil {
		return zero, err
	}

	typed, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("%w: service %q is %T, not %T", ErrServiceTypeMismatch, key.name, raw, zero)
	}
	return typed, nil
}
