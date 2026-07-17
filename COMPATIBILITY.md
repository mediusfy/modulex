# Compatibility Policy

This document describes the compatibility and support commitments for Modulex.

## Go versions

Modulex supports the two most recent stable Go releases at the time of each
tag. The `go.mod` file states the minimum Go version required to build Modulex.
Currently CI tests against Go 1.26; a version matrix covering the minimum
supported and latest stable Go releases will be added before v1.

Bumping the minimum Go version is considered a minor change during v0 and a
breaking change after v1.

## Module version compatibility

### v0.x releases

Modulex is currently in v0. While we try to keep the public API stable within
minor v0 releases, **v0 releases carry no backward-compatibility guarantee**.
Patch releases fix bugs without changing the public API. Minor releases may
introduce API changes. Consumers should pin to an exact v0 version or update
with care.

### v1.x releases

Once Modulex reaches v1, it will follow [Semantic Versioning](https://semver.org/):

- **Major releases** may introduce breaking API changes.
- **Minor releases** add functionality in a backward-compatible manner.
- **Patch releases** fix bugs without changing the public API.

The public API surface is defined by the exported identifiers in the
`github.com/mediusfy/modulex` module and its sub-packages.

## Sub-package stability

Framework adapter sub-packages (for example `modulex/nats`, `modulex/rabbitmq`,
and `modulex/watermill`) are part of the same Go module and therefore share
the module version. They follow the same compatibility policy as the core
package and are released together.

## Deprecations

After v1, deprecated APIs will be marked with a `// Deprecated:` comment at
least one minor release before they are removed. Removal only happens in a
major release. During v0 the API may evolve more quickly; deprecations will
still be noted in the changelog when practical, but the formal deprecation
window does not apply.

## Reporting compatibility issues

If an upgrade breaks your build in a way that contradicts this policy, please
open a [GitHub issue](https://github.com/mediusfy/modulex/issues) with a
minimal reproduction. See [CONTRIBUTING.md](CONTRIBUTING.md) for issue and
pull-request guidelines.
