# pricing-catalog

Open-source cloud pricing catalog and Go library for Atomity.

`pricing-catalog` has two jobs:

1. **Connect to public cloud-provider pricing/catalog sources**, normalize their data, and keep the canonical catalog up to date.
2. **Expose versioned, deterministic pricing data as a Go library** to private Atomity services.

The repository also serves as the codebase for a production-style hiring exercise: candidates can add a new provider connector and submit it as a pull request.

## Design at a glance

```text
Cloud provider pricing source
            │
            ▼
       Provider connector
            │
            ▼
    Provider-specific models
            │
            ▼
        Normalization
            │
            ▼
     Canonical pricing model
            │
       ┌────┴─────┐
       │          │
       ▼          ▼
   Validation   Snapshot
       │          │
       └────┬─────┘
            ▼
     Versioned catalog
            │
            ▼
     pricing-catalog Go API
            │
            ▼
   Atomity private repositories
```

Provider pricing is expected to change over time. Consumers should depend on a **pinned library version**, while catalog refreshes are handled by maintainers and released as new versions.

## Repository layout

```text
pkg/catalog/                Canonical model, validation, serialization, queries
pkg/connector/              Connector contract and reusable test helpers
connectors/                  Provider connectors and reference examples
cmd/pricing-catalog/         Small maintenance/validation CLI
tasks/                       Candidate assignments (one TASK.md per provider/service)
tests/                       Cross-connector tests
docs/                        Architecture and contributor documentation
internal/pipeline/           Canonical connector → validation → snapshot pipeline
internal/evaluation/         Maintainer-only evaluation entry point
.github/workflows/           Public CI
```

## Getting started

1. Open the `TASK.md` for your assigned task.
2. Read the [root README](../README.md) and the docs it links to.
3. Implement your connector under the `target_connector` path in the task's front matter.
4. Open a pull request as described in the task's Submission section.

## Quick start

```bash
make check
make validate-example
```

Run the full test suite directly:

```bash
go test ./...
```

## Consumer example

```go
package main

import (
    "os"

    "pricing-catalog/pkg/catalog"
)

func main() {
    file, _ := os.Open("catalog.json")
    snapshot, _ := catalog.LoadJSON(file)
    c, _ := catalog.FromSnapshot(snapshot)

    prices := c.Prices(catalog.PriceQuery{
        Provider: "example-cloud",
        Region:   "eu-west",
    })

    _ = prices
}
```

The public API is intentionally small. Consumers should query canonical data without knowing how any provider exposes its pricing.

## For candidates

1. Pick or receive an assignment from [`tasks/`](tasks/). Each task lives in
   `tasks/<task-id>/TASK.md` and defines the provider, service, difficulty and
   the connector directory you must implement.
2. Read the background docs, in this order:
   - [`docs/architecture.md`](docs/architecture.md)
   - [`docs/pipeline.md`](docs/pipeline.md)
   - [`docs/schema.md`](docs/schema.md)
   - [`docs/connectors.md`](docs/connectors.md)
3. Study the reference implementations:
   - [`connectors/example-simple`](connectors/example-simple)
   - [`connectors/example-complex`](connectors/example-complex)
4. Implement your connector under `connectors/<provider>/<service>/`.
5. Run `make check` before opening a pull request.
6. Submit a pull request following [`CONTRIBUTING.md`](CONTRIBUTING.md) and the
   submission section of your `TASK.md`.

Available tasks are listed in [`tasks/README.md`](tasks/README.md).

## For consumers and maintainers

- [`docs/consumer.md`](docs/consumer.md)
- [`CONTRIBUTING.md`](CONTRIBUTING.md)

## Status

This repository currently provides the **library and candidate-assignment skeleton**,
including the connector → validation → snapshot pipeline boundary.

The remaining production lifecycle is intentionally outside the candidate scope:

```text
scheduled refresh
      ↓
compare with current snapshot
      ↓
human-readable change report
      ↓
maintainer review
      ↓
release new catalog version
```

These capabilities can be added later without changing the candidate-facing
connector contract.
