# Take-Home Assignment — Add a Cloud Pricing Connector

# Assignment

Your assignment is defined in the `TASK.md` file for the task you were given:

    tasks/<task-id>/TASK.md

See [`tasks/README.md`](tasks/README.md) for the list of tasks and the
structure each one follows.

All tasks share the same expectations: use the existing connector and
normalization pipeline, do not bypass the canonical pricing model, do not
require private credentials, and document any limitation of the canonical
model rather than silently discarding information.

## Objective

Add support for the assigned cloud provider to `pricing-catalog`.

You are extending the real open-source library, not a toy project. Your connector should turn the provider's public pricing/catalog data into reliable canonical records that can eventually be consumed by production systems.

## Timebox

- Submission window: **7 calendar days**

Please do not spend the full week working continuously. The timebox is intended to give you flexibility around your schedule.

## Your task

Create your connector under:

    connectors/<provider>/<service>/

For example, an AWS EC2 assignment must be implemented under:

    connectors/aws/ec2/

The provider directory identifies the cloud provider. The service directory
identifies the provider pricing domain being implemented.

Do not place connector implementations directly under the provider directory.

## Contribution boundary

Your contribution is the **provider connector** and the supporting fixtures,
tests, and documentation for that connector.

Do not implement a separate catalog/update architecture.

In particular, do not:

- write catalog snapshots directly;
- add provider-specific persistence;
- add provider-specific scheduling;
- add a separate refresh mechanism;
- bypass the generic validation pipeline;
- change the canonical schema simply to fit the assigned provider;
- change the consumer API unless explicitly requested.

The repository already defines the pipeline boundary that your connector will
eventually plug into:

```text
connector fetch + normalization
        ↓
generic validation
        ↓
deterministic snapshot creation
```

Use the existing connector testkit. It runs your connector through this pipeline.

### 1. Understand the existing code

Before coding, review:

- `pkg/catalog`
- `pkg/connector`
- `internal/pipeline`
- `connectors/example-simple`
- `connectors/example-complex`
- `docs/schema.md`
- `docs/connectors.md`

You are expected to follow the existing conventions unless you have a good reason to change them.

### 2. Research the provider

Identify authoritative public sources for the provider's pricing/catalog data.

Prefer, in roughly this order:

1. official API;
2. official downloadable catalog/data export;
3. official structured pricing endpoint;
4. official documentation-backed data source;
5. website extraction only when no better structured source exists.

Document the source and why it is authoritative.

### 3. Understand the pricing model

Explain how the provider represents:

- products;
- SKUs;
- regions;
- purchase models;
- billing units;
- pricing dimensions;
- tiers or thresholds;
- currencies;
- important provider-specific identifiers.

### 4. Normalize the data

Map provider data to the canonical `catalog.PricingRecord` model.

**Do not lose commercially meaningful detail.**

For example, on-demand and committed prices for the same SKU are different records. Region-specific prices are different records. Distinct tiers must remain distinguishable.

Use `Dimensions` and `Attributes` when they are appropriate for preserving provider information that does not yet have a first-class canonical field.

### 5. Preserve the existing public contract

Do not change the canonical schema or consumer API simply to fit the provider. If you believe the generic model is missing an important universal concept, document the limitation and propose the change separately in `DESIGN.md`.

### 6. Add tests and fixtures

Your connector must have deterministic tests.

Add representative provider fixtures under your connector's `testdata/` directory.

Use `pkg/connector/testkit.AssertConnectorContract` as the baseline connector
test. This ensures the connector is exercised through the generic catalog
validation and snapshot-building pipeline.

At minimum, cover the important pricing cases that you discovered during research. Depending on the provider, that may include:

- multiple regions;
- multiple purchase models;
- multiple billing units;
- tiered pricing;
- optional fields;
- non-trivial pricing dimensions;
- unusual or edge-case provider records.

Run:

```bash
make check
```

### 7. Document your work

Add a short `DESIGN.md` under your connector directory.

It should cover:

- source(s) used;
- pricing model summary;
- normalization/mapping decisions;
- assumptions;
- information that could not be represented;
- known limitations;
- how to run the connector tests.

### 8. Submit a PR

Open a pull request containing your connector, tests, fixtures, and documentation.

Keep the PR focused. Do not make unrelated refactors.

## What we evaluate

We care most about:

1. **Data correctness** — are the prices and identifiers right?
2. **Schema correctness** — did you preserve meaningful pricing distinctions?
3. **Research quality** — did you find and use trustworthy sources?
4. **Testing** — did you prove the connector works beyond the happy path?
5. **Engineering judgment** — is the implementation appropriately scoped and maintainable?
6. **Documentation** — can another engineer understand your decisions?

A small, correct implementation is better than a broad implementation that silently produces incorrect data.

## AI-assisted development

AI-assisted development is permitted. You are expected to understand and take ownership of the submitted code and should disclose meaningful AI assistance.

## Optional: codebase investigation

As a secondary exercise, you may identify up to one additional genuine bug or design issue in the existing codebase.

For any issue you report, include:

```text
reproduction → root cause → proposed fix → regression test
```

Do not spend significant time hunting for unrelated issues. The connector is the primary assignment.
