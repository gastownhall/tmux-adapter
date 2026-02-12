# Architecture Standards

This repository follows these engineering constraints for all production code:

- `TDD first`: start by writing/adjusting tests, then implement until tests pass.
- `DRY`: extract shared logic rather than duplicating behavior.
- `Separation of concerns` and `SRP`: keep each module focused on one responsibility.
- `Clear contracts`: keep small interfaces and explicit behavior boundaries.
- `Low coupling / high cohesion`: reduce cross-package dependencies.
- `KISS` and `YAGNI`: build the minimum complete implementation that solves the current requirement.
- `No swallowed errors`: return or surface errors; do not silently ignore failures.
- `Observability`: log meaningful operational errors and state transitions.
- `Maintainability`: prefer readable, idiomatic Go organization over cleverness.

## Enforced Guardrails

- `go test ./...` must pass before considering a change complete.
- `go vet ./...` is part of the default local check.
- `golangci-lint` is configured via `.golangci.yml`.

Run locally:

```bash
make check
```

Optional lint pass:

```bash
make lint
```
