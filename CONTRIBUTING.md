# Contributing to Modulex

Thank you for your interest in making Modulex better. This document describes how
to build, test, and submit changes to the project.

## Getting Started

1. Clone the repository.
2. Install Go 1.26 or later.
3. Install `golangci-lint` for local linting.
4. Run `make deps` to install project dependencies.

## Development Workflow

1. Create a feature branch from `main`:
   ```bash
   git checkout -b feat/your-feature-name
   ```
2. Make your changes, following the project's coding standards.
3. Add or update tests for any new behavior.
4. Regenerate mocks if you change an interface:
   ```bash
   mockery --config .mockery.yaml
   ```
5. Format and lint your changes:
   ```bash
   make fmt
   make lint
   ```
6. Run the full test suite:
   ```bash
   make test-arch
   make test
   make build
   ```

## Pull Request Process

1. Ensure the checklist in `docs/planning/library-readiness-checklist.md` is
   considered for your change.
2. Open a pull request against `main` with a clear description and links to any
   relevant Jira tickets.
3. Pull requests must pass `make test-arch`, `make build`, `make lint`, and
   `make test` before being merged.
4. Request review from a maintainer. Address feedback before merging.
5. Maintainers will squash-merge pull requests and update `CHANGELOG.md` as
   needed.

## Coding Standards

See [`CODING_STANDARDS.md`](./CODING_STANDARDS.md) for detailed conventions on
package layout, naming, error handling, logging, tracing, testing, and
observability.

## Reporting Issues

For bugs and feature requests, open a GitHub issue with:

- A clear title and description.
- The Go version and Modulex commit you are using.
- Steps to reproduce, expected behavior, and actual behavior.

## Code of Conduct

Be respectful, constructive, and inclusive. Harassment or discriminatory
behavior will not be tolerated.
