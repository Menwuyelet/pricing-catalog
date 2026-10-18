package ec2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pricing-catalog/pkg/catalog"
	"pricing-catalog/pkg/connector"
	"pricing-catalog/pkg/connector/testkit"
)

// loadFixture reads the test fixture from testdata/source.json.
func loadFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "source.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// newFixtureConnector returns a fresh connector backed by the test fixture.
func newFixtureConnector(t *testing.T) connector.Connector {
	t.Helper()
	data := loadFixture(t)
	return New(WithReader(bytes.NewReader(data)))
}

// ---------- Contract test ----------

func TestConnectorContract(t *testing.T) {
	records := testkit.AssertConnectorContract(t, func() connector.Connector {
		return newFixtureConnector(t)
	})

	if len(records) == 0 {
		t.Fatal("expected non-empty records from contract test")
	}
}

// ---------- On-Demand parsing and normalization ----------

func TestOnDemandPriceNormalization(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find the on-demand USD record for SKU001.
	var found *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU001" && r.PurchaseModel == "on_demand" && r.Price.Currency == "USD" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("did not find on-demand USD record for SKU001")
	}

	// Verify core fields.
	if found.Provider != "aws" {
		t.Errorf("Provider: got %q, want %q", found.Provider, "aws")
	}
	if found.Region != "us-east-1" {
		t.Errorf("Region: got %q, want %q", found.Region, "us-east-1")
	}
	if found.BillingUnit != "hour" {
		t.Errorf("BillingUnit: got %q, want %q", found.BillingUnit, "hour")
	}
	if found.PricingDimension != "usage" {
		t.Errorf("PricingDimension: got %q, want %q", found.PricingDimension, "usage")
	}
	if found.Price.Amount != "0.1920000000" {
		t.Errorf("Price.Amount: got %q, want %q", found.Price.Amount, "0.1920000000")
	}
	if found.Price.Currency != "USD" {
		t.Errorf("Price.Currency: got %q, want %q", found.Price.Currency, "USD")
	}
	if found.ProviderProductID != "SKU001" {
		t.Errorf("ProviderProductID: got %q, want %q", found.ProviderProductID, "SKU001")
	}
	if found.Source.Type != "aws_price_list" {
		t.Errorf("Source.Type: got %q, want %q", found.Source.Type, "aws_price_list")
	}
	if found.Source.URL != sourceURL {
		t.Errorf("Source.URL: got %q, want %q", found.Source.URL, sourceURL)
	}
	// Product name should include instance type.
	if !strings.Contains(found.Product, "m5.xlarge") {
		t.Errorf("Product %q should contain instance type m5.xlarge", found.Product)
	}
	// Flat 0-to-Inf should not produce a tier.
	if found.Tier != nil {
		t.Errorf("Tier should be nil for flat 0-to-Inf dimension, got %+v", found.Tier)
	}
	// EffectiveFrom should be set.
	if found.EffectiveFrom == nil {
		t.Error("EffectiveFrom should be set")
	}
}

// ---------- SKU-to-term joining ----------

func TestSKUTermJoining(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Count records by SKU.
	skuCounts := make(map[string]int)
	for _, r := range records {
		skuCounts[r.SKU]++
	}

	// SKU001 should have: 2 on-demand (USD+CNY) + 2 reserved HU7G6KETJZ (upfront+hourly) + 1 reserved 4NA7Y494T4 = 5
	if skuCounts["SKU001"] != 5 {
		t.Errorf("SKU001 record count: got %d, want 5", skuCounts["SKU001"])
	}

	// SKU002 should have: 1 on-demand USD = 1
	if skuCounts["SKU002"] != 1 {
		t.Errorf("SKU002 record count: got %d, want 1", skuCounts["SKU002"])
	}

	// Out-of-scope SKU003 should not appear.
	if skuCounts["SKU003"] != 0 {
		t.Errorf("SKU003 should be excluded (out-of-scope), got %d records", skuCounts["SKU003"])
	}

	// Unmatched term SKU should not appear.
	if skuCounts["UNMATCHEDSKU"] != 0 {
		t.Errorf("UNMATCHEDSKU should be excluded (no product entry), got %d records", skuCounts["UNMATCHEDSKU"])
	}
}

// ---------- Reserved price normalization ----------

func TestReservedPriceNormalization(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find the reserved partial-upfront records for SKU001 (term HU7G6KETJZ).
	var upfrontRec, hourlyRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU != "SKU001" || r.PurchaseModel != "reserved" {
			continue
		}
		if r.Attributes == nil {
			continue
		}
		if r.Attributes["term_code"] != "HU7G6KETJZ" {
			continue
		}
		if r.PricingDimension == "upfront_fee" {
			upfrontRec = r
		} else if r.PricingDimension == "usage" {
			hourlyRec = r
		}
	}

	if upfrontRec == nil {
		t.Fatal("did not find reserved upfront fee record for SKU001 HU7G6KETJZ")
	}
	if hourlyRec == nil {
		t.Fatal("did not find reserved hourly record for SKU001 HU7G6KETJZ")
	}

	// Upfront fee checks.
	if upfrontRec.Price.Amount != "1100.0000000000" {
		t.Errorf("Upfront Amount: got %q, want %q", upfrontRec.Price.Amount, "1100.0000000000")
	}
	if upfrontRec.BillingUnit != "quantity" {
		t.Errorf("Upfront BillingUnit: got %q, want %q", upfrontRec.BillingUnit, "quantity")
	}

	// Hourly fee checks.
	if hourlyRec.Price.Amount != "0.0670000000" {
		t.Errorf("Hourly Amount: got %q, want %q", hourlyRec.Price.Amount, "0.0670000000")
	}
	if hourlyRec.BillingUnit != "hour" {
		t.Errorf("Hourly BillingUnit: got %q, want %q", hourlyRec.BillingUnit, "hour")
	}

	// Both should preserve lease/purchase attributes.
	for _, rec := range []*catalog.PricingRecord{upfrontRec, hourlyRec} {
		if rec.Attributes["lease_contract_length"] != "1yr" {
			t.Errorf("lease_contract_length: got %q, want %q", rec.Attributes["lease_contract_length"], "1yr")
		}
		if rec.Attributes["purchase_option"] != "Partial Upfront" {
			t.Errorf("purchase_option: got %q, want %q", rec.Attributes["purchase_option"], "Partial Upfront")
		}
		if rec.Attributes["offering_class"] != "standard" {
			t.Errorf("offering_class: got %q, want %q", rec.Attributes["offering_class"], "standard")
		}
	}

	// Upfront and hourly should NOT be blended — distinct ProviderSKUIDs.
	if upfrontRec.ProviderSKUID == hourlyRec.ProviderSKUID {
		t.Errorf("upfront and hourly records should have distinct ProviderSKUIDs, both are %q", upfrontRec.ProviderSKUID)
	}

	// Find the no-upfront reserved record (term 4NA7Y494T4).
	var noUpfront *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU001" && r.PurchaseModel == "reserved" &&
			r.Attributes != nil && r.Attributes["term_code"] == "4NA7Y494T4" {
			noUpfront = r
		}
	}
	if noUpfront == nil {
		t.Fatal("did not find no-upfront reserved record for SKU001")
	}
	if noUpfront.Attributes["purchase_option"] != "No Upfront" {
		t.Errorf("purchase_option: got %q, want %q", noUpfront.Attributes["purchase_option"], "No Upfront")
	}
	if noUpfront.Price.Amount != "0.1230000000" {
		t.Errorf("No Upfront Amount: got %q, want %q", noUpfront.Price.Amount, "0.1230000000")
	}
}

// ---------- Exact decimal and currency preservation ----------

func TestDecimalAndCurrencyPreservation(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find the CNY record for SKU001.
	var cnyRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU001" && r.PurchaseModel == "on_demand" && r.Price.Currency == "CNY" {
			cnyRec = r
			break
		}
	}
	if cnyRec == nil {
		t.Fatal("did not find CNY on-demand record for SKU001")
	}

	// Amount must be the exact decimal string from the source, not parsed through float64.
	if cnyRec.Price.Amount != "1.3440000000" {
		t.Errorf("CNY Amount: got %q, want %q", cnyRec.Price.Amount, "1.3440000000")
	}
	if cnyRec.Price.Currency != "CNY" {
		t.Errorf("CNY Currency: got %q, want %q", cnyRec.Price.Currency, "CNY")
	}

	// Verify original currency is retained in attributes.
	if cnyRec.Attributes["currency"] != "CNY" {
		t.Errorf("Attributes[currency]: got %q, want %q", cnyRec.Attributes["currency"], "CNY")
	}
}

// ---------- Out-of-scope product exclusion ----------

func TestOutOfScopeProductExclusion(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, r := range records {
		// No Storage product (SKU003).
		if r.SKU == "SKU003" {
			t.Error("SKU003 (Storage) should be excluded")
		}
		// No Dedicated tenancy product (SKU004).
		if r.SKU == "SKU004" {
			t.Error("SKU004 (Dedicated) should be excluded")
		}
		// No empty-region product (SKU005).
		if r.SKU == "SKU005" {
			t.Error("SKU005 (empty region) should be excluded")
		}
	}

	// Total expected: 6 records.
	// SKU001 on-demand: 2 (USD + CNY)
	// SKU002 on-demand: 1 (USD)
	// SKU001 reserved HU7G6KETJZ: 2 (upfront + hourly)
	// SKU001 reserved 4NA7Y494T4: 1
	if len(records) != 6 {
		t.Errorf("expected 6 records, got %d", len(records))
	}
}

// ---------- Malformed JSON error ----------

func TestMalformedJSONError(t *testing.T) {
	conn := New(WithReader(strings.NewReader(`{invalid json`)))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// ---------- Missing required source data ----------

func TestMissingProductsSectionError(t *testing.T) {
	// A document with no products section but with terms.
	input := `{"formatVersion":"v1.0","terms":{"OnDemand":{}}}`
	conn := New(WithReader(strings.NewReader(input)))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error when terms section appears before products")
	}
}

// ---------- HTTP non-success ----------

func TestHTTPNonSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	conn := New(WithSourceURL(srv.URL))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code 500, got: %v", err)
	}
}

// ---------- Context cancellation ----------

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// With HTTP path: should fail because the request uses the cancelled context.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not reach the server")
	}))
	defer srv.Close()

	conn := New(WithSourceURL(srv.URL))
	_, err := conn.Fetch(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Reader-backed cancellation should also be respected before parsing.
	conn = New(WithReader(bytes.NewReader(loadFixture(t))))
	_, err = conn.Fetch(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context with reader")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled for reader path, got %v", err)
	}
}

// ---------- Deterministic output ----------

func TestDeterministicOutput(t *testing.T) {
	// Run Fetch multiple times and verify identical output.
	var firstRecords []catalog.PricingRecord
	for i := 0; i < 5; i++ {
		conn := newFixtureConnector(t)
		records, err := conn.Fetch(context.Background())
		if err != nil {
			t.Fatalf("run %d: Fetch: %v", i, err)
		}
		if firstRecords == nil {
			firstRecords = records
			continue
		}
		if len(records) != len(firstRecords) {
			t.Fatalf("run %d: record count changed: %d vs %d", i, len(records), len(firstRecords))
		}
		for j := range records {
			k1 := catalog.RecordKey(firstRecords[j])
			k2 := catalog.RecordKey(records[j])
			if k1 != k2 {
				t.Fatalf("run %d record %d: RecordKey changed:\n  %s\n  %s", i, j, k1, k2)
			}
			if records[j].Price.Amount != firstRecords[j].Price.Amount {
				t.Fatalf("run %d record %d: Amount changed: %q vs %q",
					i, j, records[j].Price.Amount, firstRecords[j].Price.Amount)
			}
		}
	}
}

// ---------- No RecordKey collisions ----------

func TestNoRecordKeyCollisions(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	seen := make(map[string]int)
	for i, r := range records {
		key := catalog.RecordKey(r)
		if prev, exists := seen[key]; exists {
			t.Errorf("record %d collides with record %d under RecordKey:\n  key=%s\n  SKU=%s ProviderSKUID=%s Currency=%s",
				i, prev, key, r.SKU, r.ProviderSKUID, r.Price.Currency)
		}
		seen[key] = i
	}
}

// ---------- Network/read failure ----------

func TestNetworkFailure(t *testing.T) {
	// Use a server that immediately closes the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	conn := New(WithSourceURL(srv.URL))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

// ---------- Reader error ----------

func TestReaderError(t *testing.T) {
	conn := New(WithReader(&failingReader{}))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for reader failure")
	}
}

type failingReader struct{}

func (f *failingReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("simulated read failure")
}

// ---------- Dimensions and Attributes preservation ----------

func TestDimensionsAndAttributes(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find on-demand USD record for SKU001.
	var rec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU001" && r.PurchaseModel == "on_demand" && r.Price.Currency == "USD" {
			rec = r
			break
		}
	}
	if rec == nil {
		t.Fatal("did not find expected record")
	}

	// Verify Dimensions contains price-affecting attributes.
	expectedDims := map[string]string{
		"instance_type":          "m5.xlarge",
		"operating_system":       "Linux",
		"license_model":          "No License required",
		"tenancy":                "Shared",
		"pre_installed_sw":       "NA",
		"vcpu":                   "4",
		"memory":                 "16 GiB",
		"processor_architecture": "64-bit",
		"capacity_status":        "Used",
		"usage_type":             "BoxUsage:m5.xlarge",
		"operation":              "RunInstances",
		"region":                 "us-east-1",
		"location":               "US East (N. Virginia)",
		"current_generation":     "Yes",
	}
	for key, want := range expectedDims {
		got, ok := rec.Dimensions[key]
		if !ok {
			t.Errorf("Dimensions missing key %q", key)
		} else if got != want {
			t.Errorf("Dimensions[%q]: got %q, want %q", key, got, want)
		}
	}

	// Verify Attributes contains source metadata.
	if rec.Attributes["term_type"] != "OnDemand" {
		t.Errorf("Attributes[term_type]: got %q, want %q", rec.Attributes["term_type"], "OnDemand")
	}
	if rec.Attributes["term_code"] != "JRTCKXETXF" {
		t.Errorf("Attributes[term_code]: got %q, want %q", rec.Attributes["term_code"], "JRTCKXETXF")
	}
	if rec.Attributes["rate_code"] == "" {
		t.Error("Attributes[rate_code] should be set")
	}
}

// ---------- Multiple regions ----------

func TestMultipleRegions(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	regions := make(map[string]bool)
	for _, r := range records {
		regions[r.Region] = true
	}

	if !regions["us-east-1"] {
		t.Error("expected records for region us-east-1")
	}
	if !regions["eu-west-1"] {
		t.Error("expected records for region eu-west-1")
	}
}

// ---------- Empty document ----------

func TestEmptyProductsSection(t *testing.T) {
	input := `{
		"products": {},
		"terms": {
			"OnDemand": {},
			"Reserved": {}
		}
	}`
	conn := New(WithReader(strings.NewReader(input)))
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// No in-scope products means no records.
	if len(records) != 0 {
		t.Errorf("expected 0 records from empty products, got %d", len(records))
	}
}

// ---------- Invalid effectiveDate ----------

func TestInvalidEffectiveDateError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared",
					"operatingSystem": "Linux"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "JRTCKXETXF",
						"sku": "SKU001",
						"effectiveDate": "not-a-date",
						"priceDimensions": {
							"SKU001.JRTCKXETXF.6YS6EN2CT7": {
								"rateCode": "SKU001.JRTCKXETXF.6YS6EN2CT7",
								"description": "test",
								"unit": "Hrs",
								"beginRange": "0",
								"endRange": "Inf",
								"pricePerUnit": {"USD": "0.10"}
							}
						},
						"termAttributes": {}
					}
				}
			}
		}
	}`
	conn := New(WithReader(strings.NewReader(input)))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid effectiveDate")
	}
	if !strings.Contains(err.Error(), "effectiveDate") {
		t.Errorf("error should mention effectiveDate, got: %v", err)
	}
}

// ---------- Invalid price amount ----------

func TestInvalidPriceAmountError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared",
					"operatingSystem": "Linux"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "JRTCKXETXF",
						"sku": "SKU001",
						"effectiveDate": "2024-01-01T00:00:00Z",
						"priceDimensions": {
							"SKU001.JRTCKXETXF.6YS6EN2CT7": {
								"rateCode": "SKU001.JRTCKXETXF.6YS6EN2CT7",
								"description": "test",
								"unit": "Hrs",
								"beginRange": "0",
								"endRange": "Inf",
								"pricePerUnit": {"USD": "not-a-decimal"}
							}
						},
						"termAttributes": {}
					}
				}
			}
		}
	}`
	conn := New(WithReader(strings.NewReader(input)))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid price amount")
	}
	if !strings.Contains(err.Error(), "price.amount") {
		t.Errorf("error should mention price.amount, got: %v", err)
	}
}

// ---------- ProviderSKUID contains original components ----------

func TestProviderSKUIDComponents(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, r := range records {
		// ProviderSKUID should contain the term code, rate code, and currency.
		termCode := r.Attributes["term_code"]
		rateCode := r.Attributes["rate_code"]
		currency := r.Attributes["currency"]

		if termCode == "" || rateCode == "" || currency == "" {
			t.Errorf("record SKU=%s: attributes missing term_code/rate_code/currency", r.SKU)
			continue
		}

		expected := termCode + "|" + rateCode + "|" + currency
		if r.ProviderSKUID != expected {
			t.Errorf("ProviderSKUID: got %q, want %q", r.ProviderSKUID, expected)
		}
	}
}

// ---------- Tiered dimension ----------

func TestTieredDimension(t *testing.T) {
	// Create a fixture with a tiered dimension (non-trivial range).
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared",
					"operatingSystem": "Linux"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "JRTCKXETXF",
						"sku": "SKU001",
						"effectiveDate": "2024-01-01T00:00:00Z",
						"priceDimensions": {
							"SKU001.JRTCKXETXF.TIER1": {
								"rateCode": "SKU001.JRTCKXETXF.TIER1",
								"description": "First 100 hours",
								"unit": "Hrs",
								"beginRange": "0",
								"endRange": "100",
								"pricePerUnit": {"USD": "0.1920000000"}
							},
							"SKU001.JRTCKXETXF.TIER2": {
								"rateCode": "SKU001.JRTCKXETXF.TIER2",
								"description": "Over 100 hours",
								"unit": "Hrs",
								"beginRange": "100",
								"endRange": "Inf",
								"pricePerUnit": {"USD": "0.1500000000"}
							}
						},
						"termAttributes": {}
					}
				}
			}
		}
	}`
	conn := New(WithReader(strings.NewReader(input)))
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 tiered records, got %d", len(records))
	}

	// Find the two tier records.
	var tier1, tier2 *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.Tier != nil && r.Tier.StartQuantity == "0" && r.Tier.EndQuantity == "100" {
			tier1 = r
		} else if r.Tier != nil && r.Tier.StartQuantity == "100" && r.Tier.EndQuantity == "" {
			tier2 = r
		}
	}

	if tier1 == nil {
		t.Fatal("did not find tier 0-100")
	}
	if tier2 == nil {
		t.Fatal("did not find tier 100-Inf (open-ended)")
	}

	if tier1.Price.Amount != "0.1920000000" {
		t.Errorf("Tier1 Amount: got %q, want %q", tier1.Price.Amount, "0.1920000000")
	}
	if tier2.Price.Amount != "0.1500000000" {
		t.Errorf("Tier2 Amount: got %q, want %q", tier2.Price.Amount, "0.1500000000")
	}
}
