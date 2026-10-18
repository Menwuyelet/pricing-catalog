package ec2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type sourceProduct struct {
	SKU               string
	ProductFamily     string
	InstanceType      string
	RegionCode        string
	Location          string
	LocationType      string
	OperatingSystem   string
	LicenseModel      string
	Tenancy           string
	PreInstalledSw    string
	VCPU              string
	Memory            string
	ProcessorArch     string
	CapacityStatus    string
	UsageType         string
	Operation         string
	CurrentGeneration string
}

type sourcePriceDimension struct {
	RateCode     string
	Description  string
	Unit         string
	BeginRange   string
	EndRange     string
	PricePerUnit map[string]string
}

type sourceTerm struct {
	OfferTermCode       string
	SKU                 string
	EffectiveDate       string
	TermType            string
	LeaseContractLength string
	PurchaseOption      string
	OfferingClass       string
	PriceDimensions     []sourcePriceDimension
}

func parseProducts(ctx context.Context, dec *json.Decoder) (map[string]sourceProduct, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("products: expected opening '{': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("products: expected '{', got %v", tok)
	}

	products := make(map[string]sourceProduct)

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Read the SKU key.
		tok, err = dec.Token()
		if err != nil {
			return nil, fmt.Errorf("products: reading SKU key: %w", err)
		}
		sku, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("products: expected string SKU key, got %T", tok)
		}

		// Decode the product object.
		var raw struct {
			SKU           string `json:"sku"`
			ProductFamily string `json:"productFamily"`
			Attributes    struct {
				InstanceType      string `json:"instanceType"`
				RegionCode        string `json:"regionCode"`
				Location          string `json:"location"`
				LocationType      string `json:"locationType"`
				OperatingSystem   string `json:"operatingSystem"`
				LicenseModel      string `json:"licenseModel"`
				Tenancy           string `json:"tenancy"`
				PreInstalledSw    string `json:"preInstalledSw"`
				VCPU              string `json:"vcpu"`
				Memory            string `json:"memory"`
				ProcessorArch     string `json:"processorArchitecture"`
				CapacityStatus    string `json:"capacitystatus"`
				UsageType         string `json:"usagetype"`
				Operation         string `json:"operation"`
				CurrentGeneration string `json:"currentGeneration"`
			} `json:"attributes"`
		}
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("products: decoding SKU %q: %w", sku, err)
		}

		prod := sourceProduct{
			SKU:               raw.SKU,
			ProductFamily:     raw.ProductFamily,
			InstanceType:      raw.Attributes.InstanceType,
			RegionCode:        raw.Attributes.RegionCode,
			Location:          raw.Attributes.Location,
			LocationType:      raw.Attributes.LocationType,
			OperatingSystem:   raw.Attributes.OperatingSystem,
			LicenseModel:      raw.Attributes.LicenseModel,
			Tenancy:           raw.Attributes.Tenancy,
			PreInstalledSw:    raw.Attributes.PreInstalledSw,
			VCPU:              raw.Attributes.VCPU,
			Memory:            raw.Attributes.Memory,
			ProcessorArch:     raw.Attributes.ProcessorArch,
			CapacityStatus:    raw.Attributes.CapacityStatus,
			UsageType:         raw.Attributes.UsageType,
			Operation:         raw.Attributes.Operation,
			CurrentGeneration: raw.Attributes.CurrentGeneration,
		}

		if !isInScope(prod) {
			continue
		}

		products[sku] = prod
	}
	tok, err = dec.Token()
	if err != nil {
		return nil, fmt.Errorf("products: expected closing '}': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return nil, fmt.Errorf("products: expected '}', got %v", tok)
	}

	return products, nil
}

func isInScope(p sourceProduct) bool {
	if p.ProductFamily != "Compute Instance" {
		return false
	}
	if p.Tenancy != "Shared" {
		return false
	}
	if strings.TrimSpace(p.RegionCode) == "" {
		return false
	}
	return true
}

func parseTermCategory(ctx context.Context, dec *json.Decoder, category string, products map[string]sourceProduct, emit func(sourceTerm) error) error {
	// Expect opening '{' of the category object (keyed by SKU).
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("terms.%s: expected opening '{': %w", category, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("terms.%s: expected '{', got %v", category, tok)
	}

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Read the SKU key.
		tok, err = dec.Token()
		if err != nil {
			return fmt.Errorf("terms.%s: reading SKU key: %w", category, err)
		}
		sku, ok := tok.(string)
		if !ok {
			return fmt.Errorf("terms.%s: expected string SKU key, got %T", category, tok)
		}

		// Check if we have a product entry for this SKU.
		_, hasProduct := products[sku]

		if !hasProduct {
			// Skip the entire SKU's terms object without fully decoding it.
			if err := skipValue(dec); err != nil {
				return fmt.Errorf("terms.%s: skipping unmatched SKU %q: %w", category, sku, err)
			}
			continue
		}

		if err := parseTermsForSKU(ctx, dec, category, sku, emit); err != nil {
			return err
		}
	}
	tok, err = dec.Token()
	if err != nil {
		return fmt.Errorf("terms.%s: expected closing '}': %w", category, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("terms.%s: expected '}', got %v", category, tok)
	}

	return nil
}

// parseTermsForSKU decodes the term entries object for one SKU within a term category.
func parseTermsForSKU(ctx context.Context, dec *json.Decoder, category, sku string, emit func(sourceTerm) error) error {
	// Expect opening '{'.
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("terms.%s.%s: expected opening '{': %w", category, sku, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("terms.%s.%s: expected '{', got %v", category, sku, tok)
	}

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Read the term code key (e.g., "JRTCKXETXF.JRTCKXETXF").
		tok, err = dec.Token()
		if err != nil {
			return fmt.Errorf("terms.%s.%s: reading term key: %w", category, sku, err)
		}
		if _, ok := tok.(string); !ok {
			return fmt.Errorf("terms.%s.%s: expected string term key, got %T", category, sku, tok)
		}

		// Decode the individual term entry.
		var rawTerm struct {
			OfferTermCode   string `json:"offerTermCode"`
			SKU             string `json:"sku"`
			EffectiveDate   string `json:"effectiveDate"`
			PriceDimensions map[string]struct {
				RateCode     string            `json:"rateCode"`
				Description  string            `json:"description"`
				Unit         string            `json:"unit"`
				BeginRange   string            `json:"beginRange"`
				EndRange     string            `json:"endRange"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
			TermAttributes map[string]string `json:"termAttributes"`
		}
		if err := dec.Decode(&rawTerm); err != nil {
			return fmt.Errorf("terms.%s.%s: decoding term: %w", category, sku, err)
		}
		if rawTerm.SKU == "" {
			return fmt.Errorf("terms.%s.%s: missing sku", category, sku)
		}
		if rawTerm.SKU != sku {
			return fmt.Errorf("terms.%s.%s: term sku %q does not match outer sku %q", category, sku, rawTerm.SKU, sku)
		}
		if rawTerm.OfferTermCode == "" {
			return fmt.Errorf("terms.%s.%s: missing offerTermCode", category, sku)
		}
		if len(rawTerm.PriceDimensions) == 0 {
			return fmt.Errorf("terms.%s.%s: missing priceDimensions", category, sku)
		}

		term := sourceTerm{
			OfferTermCode: rawTerm.OfferTermCode,
			SKU:           rawTerm.SKU,
			EffectiveDate: rawTerm.EffectiveDate,
			TermType:      category,
		}

		if rawTerm.TermAttributes != nil {
			term.LeaseContractLength = rawTerm.TermAttributes["LeaseContractLength"]
			term.PurchaseOption = rawTerm.TermAttributes["PurchaseOption"]
			term.OfferingClass = rawTerm.TermAttributes["OfferingClass"]
		}

		// Collect price dimensions in a deterministic order.
		dimKeys := sortedKeys(rawTerm.PriceDimensions)
		for _, rateCode := range dimKeys {
			dim := rawTerm.PriceDimensions[rateCode]
			if dim.RateCode == "" {
				return fmt.Errorf("terms.%s.%s: missing rateCode for price dimension %q", category, sku, rateCode)
			}
			if len(dim.PricePerUnit) == 0 {
				return fmt.Errorf("terms.%s.%s rate %s: missing pricePerUnit", category, sku, dim.RateCode)
			}
			term.PriceDimensions = append(term.PriceDimensions, sourcePriceDimension{
				RateCode:     dim.RateCode,
				Description:  dim.Description,
				Unit:         dim.Unit,
				BeginRange:   dim.BeginRange,
				EndRange:     dim.EndRange,
				PricePerUnit: dim.PricePerUnit,
			})
		}

		if err := emit(term); err != nil {
			return err
		}
	}
	tok, err = dec.Token()
	if err != nil {
		return fmt.Errorf("terms.%s.%s: expected closing '}': %w", category, sku, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("terms.%s.%s: expected '}', got %v", category, sku, tok)
	}

	return nil
}

func parseTerms(ctx context.Context, dec *json.Decoder, products map[string]sourceProduct, emit func(sourceTerm) error) error {
	// Expect opening '{' of the terms object.
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("terms: expected opening '{': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("terms: expected '{', got %v", tok)
	}

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Read the category key ("OnDemand" or "Reserved").
		tok, err = dec.Token()
		if err != nil {
			return fmt.Errorf("terms: reading category key: %w", err)
		}
		categoryName, ok := tok.(string)
		if !ok {
			return fmt.Errorf("terms: expected string category key, got %T", tok)
		}

		switch categoryName {
		case "OnDemand", "Reserved":
			if err := parseTermCategory(ctx, dec, categoryName, products, emit); err != nil {
				return err
			}
		default:
			// Skip unsupported term categories (e.g. future additions).
			if err := skipValue(dec); err != nil {
				return fmt.Errorf("terms: skipping category %q: %w", categoryName, err)
			}
		}
	}

	tok, err = dec.Token()
	if err != nil {
		return fmt.Errorf("terms: expected closing '}': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return fmt.Errorf("terms: expected '}', got %v", tok)
	}

	return nil
}

func decodeOfferFile(ctx context.Context, r io.Reader) (map[string]sourceProduct, []sourceTerm, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("offer file: expected opening '{': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("offer file: expected '{', got %v", tok)
	}

	var products map[string]sourceProduct
	var terms []sourceTerm

	for dec.More() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		// Read the section key.
		tok, err = dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("offer file: reading section key: %w", err)
		}
		sectionName, ok := tok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("offer file: expected string section key, got %T", tok)
		}

		switch sectionName {
		case "products":
			products, err = parseProducts(ctx, dec)
			if err != nil {
				return nil, nil, err
			}

		case "terms":
			if products == nil {
				return nil, nil, fmt.Errorf("offer file: 'terms' section found before 'products'")
			}
			err = parseTerms(ctx, dec, products, func(t sourceTerm) error {
				terms = append(terms, t)
				return nil
			})
			if err != nil {
				return nil, nil, err
			}

		default:
			// Skip other sections (formatVersion, disclaimer, offerCode, version, publicationDate).
			if err := skipValue(dec); err != nil {
				return nil, nil, fmt.Errorf("offer file: skipping section %q: %w", sectionName, err)
			}
		}
	}

	tok, err = dec.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("offer file: expected closing '}': %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return nil, nil, fmt.Errorf("offer file: expected '}', got %v", tok)
	}

	if products == nil {
		return nil, nil, fmt.Errorf("offer file: missing 'products' section")
	}

	return products, terms, nil
}

func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	switch tok := tok.(type) {
	case json.Delim:
		switch tok {
		case '{':
			for dec.More() {
				// Skip key.
				if _, err := dec.Token(); err != nil {
					return err
				}
				// Skip value.
				if err := skipValue(dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		case '[':
			for dec.More() {
				if err := skipValue(dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		}
	default:
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
