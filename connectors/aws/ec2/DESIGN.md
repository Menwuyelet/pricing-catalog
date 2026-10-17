# AWS EC2 Pricing Connector Design

## Status and scope

**Status:** Normative implementation baseline. The implementation and tests must follow these decisions; any justified deviation must be documented here and covered by tests before the behavior changes.

**Target package:** `connectors/aws/ec2`

**Connector name:** `aws-ec2`

This connector converts AWS's public Amazon EC2 offer price list into `catalog.PricingRecord` values. It is responsible for source retrieval, source parsing, scope selection, and provider-to-canonical normalization. The repository's existing pipeline remains responsible for canonical validation, ordering, and snapshot creation.

### Supported scope

Include products whose AWS product attributes identify them as `productFamily = "Compute Instance"` and `tenancy = "Shared"`, provided the product has a usable AWS region code. Preserve the source attributes needed to distinguish price-affecting variants.

Support the `OnDemand` and `Reserved` term groups in the Amazon EC2 offer file. Emit records for every supported price dimension and currency found in those terms, including recurring and one-time/upfront components. Do not restrict the implementation to Linux or a particular instance family.

Explicitly out of scope:

- Spot pricing.
- Savings Plans, which are not represented as an ordinary `OnDemand` or `Reserved` term in this offer file and require separate source/model handling.
- Dedicated tenancy and dedicated hosts.
- Non-compute EC2 product families, including storage and data transfer.
- Private, negotiated, or customer-specific prices.

Products that cannot be mapped to a required canonical field (in particular, a usable region) are excluded rather than assigned a fabricated value. Scope filtering must be based on source attributes, not product display-name matching.

## Authoritative source and retrieval

Use the public AWS Price List Bulk API offer file for Amazon EC2:

- Current offer: <https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/index.json>
- AWS Price List Bulk API documentation: <https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/using-the-aws-price-list-bulk-api.html>
- AWS price-list JSON structure: <https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/reading-service-price-list-file-for-services.html>

The `current` URL is intentionally used for live fetches and may change over time. Every record's `Source.URL` must identify this offer URL. Tests must use fixed local fixtures and must not contact AWS.

The source is public; no AWS SDK session, access key, secret key, or other AWS credentials are required. Use an HTTP client with a bounded timeout, issue requests with the supplied context, require a successful HTTP status, and close response bodies. Fetch, HTTP-status, read, and decode failures must be returned as errors. The connector must not turn a failure into a successful empty result or return partially decoded records as success.

### Memory strategy

Do not read the response into a full raw `[]byte` and then decode that byte slice into a second full in-memory representation. Decode from the response stream. The AWS document separates `products` and `terms`, so the implementation must retain enough product metadata to join terms by SKU. Prefer a token/section-based decoder that stores only the scoped product metadata needed for normalization and processes term entries incrementally. Do not retain the entire decoded `terms` tree.

The output `[]catalog.PricingRecord` is necessarily retained because it is the connector interface's return value. If source inspection or measurements show the token-based approach adds disproportionate complexity for the assignment, document the measured trade-off before replacing it with whole-document decoding; do not silently introduce an unbounded raw-body copy.

## Source data model

The offer file contains three relevant levels:

1. `products`, keyed by product SKU. Product metadata includes `productFamily` and attributes such as `instanceType`, `regionCode`, `location`, `locationType`, `operatingSystem`, `licenseModel`, `tenancy`, `preInstalledSw`, `vcpu`, `memory`, `processorArchitecture`, `capacitystatus`, `usageType`, and `operation` when supplied.
2. `terms`, grouped by term category (`OnDemand` or `Reserved`), then SKU, then term code.
3. `priceDimensions`, grouped within a term by rate code. Each dimension provides a description, unit, range boundaries, and `pricePerUnit` amounts keyed by currency.

Join terms to product metadata by SKU. A term whose SKU has no product entry cannot be normalized reliably and must be skipped without fabricating product metadata. Malformed required structure must produce a contextual error rather than a partial successful catalog.

## Canonical mapping decisions

The implementation must use the repository's existing `catalog.PricingRecord` schema without modifying the shared schema or consumer API.

| Canonical field | Required mapping |
|---|---|
| `Provider` | `aws` |
| `Product` | Stable readable name derived from the product attributes; include the instance type when present. Do not use a display label as the only source of product identity. |
| `SKU` | AWS product SKU exactly as supplied. |
| `ProviderProductID` | AWS product SKU. |
| `ProviderSKUID` | A deterministic composite pricing identifier derived from the AWS term code, rate code, and currency. Preserve each original component separately in `Attributes`. This prevents distinct term/rate/currency records from collapsing under the repository's `RecordKey`, which does not include `Attributes`, price amount, or currency. |
| `Region` | AWS `regionCode`. Do not infer a region from a human-readable location. Exclude a product when `regionCode` is missing or empty because the canonical field is required. |
| `PurchaseModel` | `on_demand` for `OnDemand`; `reserved` for `Reserved`. |
| `BillingUnit` | Normalize known units consistently: `Hrs` to `hour`; `Quantity` to `quantity`. Preserve other non-empty units in a deterministic normalized form rather than silently dropping their prices. A `quantity` unit must not be represented as an hourly rate. |
| `PricingDimension` | Stable semantic category: `usage` for recurring usage dimensions, `upfront_fee` for a one-time upfront charge, and a distinct stable category for any other one-time/non-recurring dimension. Derive this from term/rate metadata and source description using explicit tested rules; retain the original description and unit in `Attributes`. |
| `Dimensions` | Preserve available price-affecting product attributes, including instance type, operating system, license model, tenancy, preinstalled software, operation, usage type, capacity status, processor architecture, vCPU, and memory. Include region-related descriptive metadata when useful. Use stable canonical keys and values. |
| `Price.Amount` | Exact decimal string from `pricePerUnit`; never parse through floating point. |
| `Price.Currency` | Currency key from `pricePerUnit`, normalized to uppercase only if necessary. Emit one record per currency. Do not assume USD is the only currency. |
| `Tier` | Populate when the source describes a meaningful quantity range. Preserve decimal boundaries as strings; represent an open-ended `Inf` end with an empty `EndQuantity`. Do not create a tier for a flat, non-tiered `0`-to-`Inf` price dimension. |
| `EffectiveFrom` | Parse term `effectiveDate` when present and valid. If present but malformed, return a contextual decode/normalization error. |
| `EffectiveTo` | Leave unset unless the source provides an explicit end date for that price record. Do not infer it from the current date or a later source record. |
| `Source.Type` | `aws_price_list` |
| `Source.URL` | The public Amazon EC2 current offer URL above. |
| `Attributes` | Preserve original `termType`, `termCode`, `rateCode`, term attributes (including `LeaseContractLength`, `PurchaseOption`, and `OfferingClass` when present), original price-dimension description/unit, and other useful source metadata not represented elsewhere. |

The composite `ProviderSKUID` must be constructed using an unambiguous deterministic encoding, not ad hoc concatenation that could collide. The original term code, rate code, and currency must remain separately available in `Attributes` for traceability. `Dimensions` and `Attributes` maps must be newly constructed per record and populated without relying on Go map iteration order.

### Price components and tiers

Each price dimension and each currency amount becomes its own record. A Reserved term can contain both upfront and recurring components; emit separate records and never blend, amortize, or convert an upfront charge into an hourly price. Preserve lease length, purchase option, and offering class because these can distinguish commercially different Reserved offers for the same product SKU.

The canonical `Tier` represents quantity ranges, not Reserved commitment metadata. Reserved term attributes belong in `Attributes`, not in `Tier`.

If a supported price dimension contains an unexpected but non-empty unit or a valid currency amount, preserve it faithfully using the mapping rules above. If an amount is missing, malformed, or cannot satisfy canonical validation, return an error with SKU/term/rate context rather than silently dropping that price dimension. Empty/missing optional attributes may be omitted; required identity or pricing fields may not be fabricated.

## Determinism and error handling

- Normalize source maps into canonical records with stable field names and values.
- Sort the returned records by a stable key before returning. Do not rely on Go map iteration order.
- Preserve separate records for distinct SKU, region, purchase model, term/rate component, currency, and meaningful tier.
- Respect context cancellation during HTTP retrieval and while iterating large source sections where practical.
- Return errors for request failures, non-2xx status codes, malformed JSON, malformed required source structures, invalid price amounts, invalid effective dates, and normalization failures. Include enough SKU/term/rate context to diagnose source issues.
- Do not change `pkg/catalog`, `pkg/connector`, the generic pipeline, or the consumer API to accommodate AWS-specific details.

## Package and implementation structure

Keep implementation details private to `connectors/aws/ec2` unless repository conventions require otherwise. The package should expose a constructor for the connector and implement `connector.Connector` (`Name()` and `Fetch(ctx)`). Separate concerns into small helpers/types for:

1. HTTP source retrieval and response/status handling.
2. AWS source structures or streaming JSON decoding.
3. Product scope filtering and SKU association.
4. Price dimension/currency expansion.
5. Canonical mapping, unit/dimension classification, and deterministic sorting.

Allow the HTTP client and source URL (or an equivalent source reader abstraction) to be injected for tests. Production defaults must use the public current offer URL. Tests should not require live network access.

## Required tests and fixtures

Place deterministic fixtures under `connectors/aws/ec2/testdata/`. Keep fixtures small and representative; include source fields needed to demonstrate each case rather than copying the entire AWS offer file.

Required coverage:

- Contract test using `pkg/connector/testkit.AssertConnectorContract` with a fresh connector/reader per run.
- On-Demand hourly price.
- Reserved terms with differing lease/purchase attributes.
- Separate upfront and recurring Reserved price dimensions.
- Multiple regions and preservation of instance/OS/license or other meaningful product variants.
- Multiple currencies when present in the fixture.
- A meaningful tiered dimension and a non-tiered dimension.
- Optional product attributes and products outside the supported scope.
- Product/term SKU association, including an unmatched term SKU.
- Deterministic output independent of input map order.
- Malformed JSON, malformed required pricing data, HTTP non-success, network/read failure, and context cancellation.
- Validation that no records collide under `catalog.RecordKey` for distinct term/rate/currency combinations.

Run focused tests with `go test ./connectors/aws/ec2`, then the repository check `make check`. Tests must use fixtures or injected test servers and must not depend on AWS availability or mutable live prices.

## Known limitations

- The `current` source is mutable; the connector returns the latest published public offer data, while fixtures make tests reproducible.
- The connector represents AWS's public list prices, not negotiated discounts, private offers, customer-specific rates, or a final bill.
- Spot, Savings Plans, dedicated tenancy/hosts, and non-compute EC2 families are outside this iteration.
- The canonical schema has no first-class fields for all AWS term/rate identifiers or commitment attributes. These are retained in `ProviderSKUID` and `Attributes` without changing the shared schema.
- The required canonical `Region` field means regionless/global products cannot be represented in this scope and are excluded rather than assigned a fake region.
- Unit normalization and pricing-dimension classification must remain explicit and tested. Unknown non-empty units must not be silently coerced into hours.
