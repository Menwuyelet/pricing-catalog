package ec2

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"pricing-catalog/pkg/catalog"
)

const (
	providerName = "aws"
	sourceType   = "aws_price_list"
	sourceURL    = "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/index.json"
)

func normalizeRecords(products map[string]sourceProduct, terms []sourceTerm) ([]catalog.PricingRecord, error) {
	var records []catalog.PricingRecord

	for _, term := range terms {
		prod, ok := products[term.SKU]
		if !ok {
			continue
		}

		purchaseModel, err := normalizePurchaseModel(term.TermType)
		if err != nil {
			return nil, fmt.Errorf("SKU %s term %s: %w", term.SKU, term.OfferTermCode, err)
		}

		var effectiveFrom *time.Time
		if term.EffectiveDate != "" {
			t, err := time.Parse(time.RFC3339, term.EffectiveDate)
			if err != nil {
				return nil, fmt.Errorf("SKU %s term %s: invalid effectiveDate %q: %w",
					term.SKU, term.OfferTermCode, term.EffectiveDate, err)
			}
			tUTC := t.UTC()
			effectiveFrom = &tUTC
		}

		productName := buildProductName(prod)

		for _, dim := range term.PriceDimensions {
			billingUnit, err := normalizeBillingUnit(dim.Unit)
			if err != nil {
				return nil, fmt.Errorf("SKU %s term %s rate %s: %w",
					term.SKU, term.OfferTermCode, dim.RateCode, err)
			}

			pricingDimension := classifyPricingDimension(dim, billingUnit)

			tier := buildTier(dim)

			currencies := sortedKeys(dim.PricePerUnit)
			if len(currencies) == 0 {
				return nil, fmt.Errorf("SKU %s term %s rate %s: no currency amounts in pricePerUnit",
					term.SKU, term.OfferTermCode, dim.RateCode)
			}

			for _, currency := range currencies {
				amount := dim.PricePerUnit[currency]
				if amount == "" {
					return nil, fmt.Errorf("SKU %s term %s rate %s currency %s: empty price amount",
						term.SKU, term.OfferTermCode, dim.RateCode, currency)
				}

				normalizedCurrency := strings.ToUpper(currency)

				providerSKUID := buildProviderSKUID(term.OfferTermCode, dim.RateCode, normalizedCurrency)

				dimensions := buildDimensions(prod)
				attributes := buildAttributes(term, dim, normalizedCurrency)

				rec := catalog.PricingRecord{
					Provider:          providerName,
					Product:           productName,
					SKU:               term.SKU,
					ProviderProductID: term.SKU,
					ProviderSKUID:     providerSKUID,
					Region:            prod.RegionCode,
					PurchaseModel:     purchaseModel,
					BillingUnit:       billingUnit,
					PricingDimension:  pricingDimension,
					Dimensions:        dimensions,
					Price: catalog.Money{
						Amount:   amount,
						Currency: normalizedCurrency,
					},
					Tier:          tier,
					EffectiveFrom: effectiveFrom,
					Source: catalog.Source{
						Type: sourceType,
						URL:  sourceURL,
					},
					Attributes: attributes,
				}

				if err := catalog.ValidateRecord(rec); err != nil {
					return nil, fmt.Errorf("SKU %s term %s rate %s currency %s: %w",
						term.SKU, term.OfferTermCode, dim.RateCode, normalizedCurrency, err)
				}

				records = append(records, rec)
			}
		}
	}

	catalog.SortRecords(records)

	return records, nil
}

func buildProductName(p sourceProduct) string {
	parts := []string{"EC2"}
	if p.InstanceType != "" {
		parts = append(parts, p.InstanceType)
	}
	if p.OperatingSystem != "" {
		parts = append(parts, p.OperatingSystem)
	}
	return strings.Join(parts, " ")
}

func normalizePurchaseModel(termType string) (string, error) {
	switch termType {
	case "OnDemand":
		return "on_demand", nil
	case "Reserved":
		return "reserved", nil
	default:
		return "", fmt.Errorf("unsupported term type %q", termType)
	}
}

func normalizeBillingUnit(unit string) (string, error) {
	trimmed := strings.TrimSpace(unit)
	if trimmed == "" {
		return "", fmt.Errorf("empty billing unit")
	}
	switch trimmed {
	case "Hrs":
		return "hour", nil
	case "Quantity":
		return "quantity", nil
	default:
		return strings.ToLower(trimmed), nil
	}
}

func classifyPricingDimension(dim sourcePriceDimension, billingUnit string) string {
	desc := strings.ToLower(dim.Description)

	if strings.Contains(desc, "upfront") {
		return "upfront_fee"
	}

	if billingUnit == "quantity" {
		return "one_time_fee"
	}

	return "usage"
}

func buildTier(dim sourcePriceDimension) *catalog.Tier {
	begin := dim.BeginRange
	end := dim.EndRange

	if begin == "" {
		begin = "0"
	}
	if end == "" {
		end = "Inf"
	}

	if begin == "0" && end == "Inf" {
		return nil
	}

	endQuantity := ""
	if end != "Inf" {
		endQuantity = end
	}

	return &catalog.Tier{
		StartQuantity: begin,
		EndQuantity:   endQuantity,
	}
}

func buildProviderSKUID(termCode, rateCode, currency string) string {
	return termCode + "|" + rateCode + "|" + currency
}

func buildDimensions(p sourceProduct) map[string]string {
	dims := make(map[string]string)

	setIfNonEmpty := func(key, value string) {
		if value != "" {
			dims[key] = value
		}
	}

	setIfNonEmpty("instance_type", p.InstanceType)
	setIfNonEmpty("operating_system", p.OperatingSystem)
	setIfNonEmpty("license_model", p.LicenseModel)
	setIfNonEmpty("tenancy", p.Tenancy)
	setIfNonEmpty("pre_installed_sw", p.PreInstalledSw)
	setIfNonEmpty("operation", p.Operation)
	setIfNonEmpty("usage_type", p.UsageType)
	setIfNonEmpty("capacity_status", p.CapacityStatus)
	setIfNonEmpty("processor_architecture", p.ProcessorArch)
	setIfNonEmpty("vcpu", p.VCPU)
	setIfNonEmpty("memory", p.Memory)
	setIfNonEmpty("region", p.RegionCode)
	setIfNonEmpty("location", p.Location)
	setIfNonEmpty("current_generation", p.CurrentGeneration)

	if len(dims) == 0 {
		return nil
	}
	return dims
}

func buildAttributes(term sourceTerm, dim sourcePriceDimension, currency string) map[string]string {
	attrs := make(map[string]string)

	setIfNonEmpty := func(key, value string) {
		if value != "" {
			attrs[key] = value
		}
	}

	setIfNonEmpty("term_type", term.TermType)
	setIfNonEmpty("term_code", term.OfferTermCode)
	setIfNonEmpty("rate_code", dim.RateCode)
	setIfNonEmpty("currency", currency)
	setIfNonEmpty("description", dim.Description)
	setIfNonEmpty("unit", dim.Unit)
	setIfNonEmpty("lease_contract_length", term.LeaseContractLength)
	setIfNonEmpty("purchase_option", term.PurchaseOption)
	setIfNonEmpty("offering_class", term.OfferingClass)

	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

func sortStrings(s []string) {
	sort.Strings(s)
}
