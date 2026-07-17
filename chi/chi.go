package chi

import (
	gochi "github.com/go-chi/chi/v5"

	"github.com/mediusfy/modulex"
)

// RouterKey is the typed service key used to register and resolve a Chi router.
var RouterKey = modulex.NewKey[gochi.Router]("chi.Router")

// RegisterRouter registers a Chi router in the registry so modules can resolve
// it without importing modulex/chi in the composition root.
func RegisterRouter(reg modulex.Registry, router gochi.Router) error {
	return modulex.Provide(reg, RouterKey, router)
}

// ResolveRouter retrieves the Chi router registered by RegisterRouter.
func ResolveRouter(reg modulex.Registry) (gochi.Router, error) {
	return modulex.Resolve(reg, RouterKey)
}
