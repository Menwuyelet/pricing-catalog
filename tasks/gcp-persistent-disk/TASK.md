---
id: gcp-persistent-disk
provider: gcp
service: persistent-disk
difficulty: easy
target_connector: connectors/gcp/persistent-disk/
---

# Google Persistent Disk Pricing Connector

## Assignment

Implement a pricing connector for the assigned Google Persistent Disk pricing domain.

The connector must integrate with the existing connector and normalization
pipeline and produce canonical pricing records for the supported scope.

## Target connector

The implementation must live under:

    connectors/gcp/persistent-disk/

Follow the repository's connector directory convention.

## Scope

Block storage disks and snapshots.

Determine the appropriate pricing scope from the authoritative Google Cloud sources
you research.

The implementation should cover the meaningful pricing distinctions exposed
by those sources rather than reducing the provider data to a simplified
price-only representation.

Areas that commonly carry pricing distinctions in this domain (non-exhaustive;
confirm each against the source rather than assuming):

- Disk types and provisioned capacity or performance
- Snapshot storage
- Regional versus zonal replication

## Research requirements

Before implementing the connector:

- identify the authoritative public pricing source(s);
- understand the provider's pricing model;
- identify the relevant product and SKU identifiers;
- identify applicable regions;
- identify billing units and pricing dimensions;
- identify applicable purchase models and tiers;
- document important assumptions and limitations.

## Functional requirements

The connector must:

- normalize provider data into the canonical pricing model;
- preserve meaningful product and SKU distinctions;
- preserve applicable regions;
- preserve billing units;
- preserve applicable purchase models;
- preserve pricing dimensions and tiers where applicable;
- retain relevant provider identifiers;
- produce deterministic output;
- satisfy the generic connector contract.

## Constraints

- Use the existing connector and normalization pipeline.
- Do not bypass the canonical pricing model.
- Do not add provider-specific behavior to the consumer API.
- Do not require private credentials.
- Do not commit private evaluation data.

If the existing canonical model cannot represent a commercially meaningful
pricing distinction, document the limitation rather than silently discarding
the information.

## Deliverables

Submit:

- the connector implementation;
- realistic deterministic fixtures;
- unit tests;
- connector contract tests;
- documentation describing the source and mapping decisions.

## Submission

Submit the work as a pull request against the repository.

The pull request should clearly describe:

- the authoritative pricing source(s) used;
- the pricing model understood by the implementation;
- important normalization decisions;
- assumptions and limitations;
- test coverage.
