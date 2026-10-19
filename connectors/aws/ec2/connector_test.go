package ec2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pricing-catalog/internal/evaluation/runner"
	"pricing-catalog/internal/pipeline"
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

	// SKU001 should have: 2 on-demand (USD+CNY) + 2 reserved HU7G6KETJZ (upfront+hourly)
	//   + 1 reserved 4NA7Y494T4 + 2 reserved 38NPMPTW36 (upfront+hourly) + 2 reserved CUZHX8X6JH (upfront+hourly) = 9
	if skuCounts["SKU001"] != 9 {
		t.Errorf("SKU001 record count: got %d, want 9", skuCounts["SKU001"])
	}

	// SKU002 should have: 1 on-demand USD + 2 reserved 6QCMYABX3D (upfront+hourly) = 3
	if skuCounts["SKU002"] != 3 {
		t.Errorf("SKU002 record count: got %d, want 3", skuCounts["SKU002"])
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

	// Total expected: 18 records.
	// SKU001 on-demand: 2 (USD + CNY)
	// SKU002 on-demand: 1 (USD)
	// SKU006 on-demand: 1 (USD)
	// SKU007 on-demand: 2 (tiered USD)
	// SKU008 on-demand: 1 (USD)
	// SKU009 on-demand: 1 (USD)
	// SKU010 on-demand: 1 (USD)
	// SKU001 reserved HU7G6KETJZ: 2 (upfront + hourly)
	// SKU001 reserved 4NA7Y494T4: 1
	// SKU001 reserved 38NPMPTW36: 2 (upfront + $0 hourly)
	// SKU001 reserved CUZHX8X6JH: 2 (upfront + hourly)
	// SKU002 reserved 6QCMYABX3D: 2 (upfront + $0 hourly)
	if len(records) != 18 {
		t.Errorf("expected 18 records, got %d", len(records))
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
	if !regions["ap-southeast-1"] {
		t.Error("expected records for region ap-southeast-1")
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

// ---------- Day 3: Expanded pricing coverage tests ----------

// ---------- Multiple instance types ----------

func TestMultipleInstanceTypes(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	instanceTypes := make(map[string]bool)
	for _, r := range records {
		if it, ok := r.Dimensions["instance_type"]; ok {
			instanceTypes[it] = true
		}
	}

	expected := []string{"m5.xlarge", "c5.large", "t3.micro", "r5.large", "m4.large"}
	for _, it := range expected {
		if !instanceTypes[it] {
			t.Errorf("expected records with instance_type %q", it)
		}
	}
}

// ---------- Multiple operating systems ----------

func TestMultipleOperatingSystems(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	osTypes := make(map[string]bool)
	for _, r := range records {
		if os, ok := r.Dimensions["operating_system"]; ok {
			osTypes[os] = true
		}
	}

	if !osTypes["Linux"] {
		t.Error("expected records with operating_system Linux")
	}
	if !osTypes["Windows"] {
		t.Error("expected records with operating_system Windows")
	}
	if !osTypes["RHEL"] {
		t.Error("expected records with operating_system RHEL")
	}

	// Product names should include OS.
	var linuxRec, windowsRec, rhelRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU001" && r.PurchaseModel == "on_demand" && r.Price.Currency == "USD" {
			linuxRec = r
		}
		if r.SKU == "SKU002" && r.PurchaseModel == "on_demand" {
			windowsRec = r
		}
		if r.SKU == "SKU008" && r.PurchaseModel == "on_demand" {
			rhelRec = r
		}
	}

	if linuxRec == nil || !strings.Contains(linuxRec.Product, "Linux") {
		t.Error("Linux product name should contain 'Linux'")
	}
	if windowsRec == nil || !strings.Contains(windowsRec.Product, "Windows") {
		t.Error("Windows product name should contain 'Windows'")
	}
	if rhelRec == nil || !strings.Contains(rhelRec.Product, "RHEL") {
		t.Error("RHEL product name should contain 'RHEL'")
	}
}

// ---------- All Upfront Reserved ----------

func TestAllUpfrontReserved(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find All Upfront reserved records for SKU001 (term 38NPMPTW36).
	var upfrontRec, hourlyRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU != "SKU001" || r.PurchaseModel != "reserved" {
			continue
		}
		if r.Attributes == nil || r.Attributes["term_code"] != "38NPMPTW36" {
			continue
		}
		if r.PricingDimension == "upfront_fee" {
			upfrontRec = r
		} else if r.PricingDimension == "usage" {
			hourlyRec = r
		}
	}

	if upfrontRec == nil {
		t.Fatal("did not find All Upfront upfront fee record for SKU001 38NPMPTW36")
	}
	if hourlyRec == nil {
		t.Fatal("did not find All Upfront hourly record for SKU001 38NPMPTW36")
	}

	// Upfront fee checks.
	if upfrontRec.Price.Amount != "8280.0000000000" {
		t.Errorf("All Upfront upfront Amount: got %q, want %q", upfrontRec.Price.Amount, "8280.0000000000")
	}
	if upfrontRec.BillingUnit != "quantity" {
		t.Errorf("All Upfront upfront BillingUnit: got %q, want %q", upfrontRec.BillingUnit, "quantity")
	}

	// Hourly should be $0 — it's still emitted, not collapsed.
	if hourlyRec.Price.Amount != "0.0000000000" {
		t.Errorf("All Upfront hourly Amount: got %q, want %q", hourlyRec.Price.Amount, "0.0000000000")
	}
	if hourlyRec.BillingUnit != "hour" {
		t.Errorf("All Upfront hourly BillingUnit: got %q, want %q", hourlyRec.BillingUnit, "hour")
	}

	// Both must preserve purchase attributes.
	if upfrontRec.Attributes["purchase_option"] != "All Upfront" {
		t.Errorf("purchase_option: got %q, want %q", upfrontRec.Attributes["purchase_option"], "All Upfront")
	}
	if hourlyRec.Attributes["purchase_option"] != "All Upfront" {
		t.Errorf("hourly purchase_option: got %q, want %q", hourlyRec.Attributes["purchase_option"], "All Upfront")
	}

	// Upfront and hourly must have distinct ProviderSKUIDs.
	if upfrontRec.ProviderSKUID == hourlyRec.ProviderSKUID {
		t.Errorf("All Upfront upfront and hourly should have distinct ProviderSKUIDs, both are %q", upfrontRec.ProviderSKUID)
	}
}

// ---------- Three-year Reserved ----------

func TestThreeYearReserved(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find a 3yr reserved record (term 38NPMPTW36).
	var found bool
	for _, r := range records {
		if r.SKU == "SKU001" && r.PurchaseModel == "reserved" &&
			r.Attributes != nil && r.Attributes["term_code"] == "38NPMPTW36" {
			if r.Attributes["lease_contract_length"] != "3yr" {
				t.Errorf("lease_contract_length: got %q, want %q", r.Attributes["lease_contract_length"], "3yr")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("did not find 3yr reserved record for SKU001")
	}
}

// ---------- Convertible offering class ----------

func TestConvertibleOfferingClass(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find convertible reserved records for SKU001 (term CUZHX8X6JH).
	var upfrontRec, hourlyRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU != "SKU001" || r.PurchaseModel != "reserved" {
			continue
		}
		if r.Attributes == nil || r.Attributes["term_code"] != "CUZHX8X6JH" {
			continue
		}
		if r.PricingDimension == "upfront_fee" {
			upfrontRec = r
		} else if r.PricingDimension == "usage" {
			hourlyRec = r
		}
	}

	if upfrontRec == nil {
		t.Fatal("did not find convertible upfront fee record")
	}
	if hourlyRec == nil {
		t.Fatal("did not find convertible hourly record")
	}

	// Verify offering class is preserved.
	if upfrontRec.Attributes["offering_class"] != "convertible" {
		t.Errorf("offering_class: got %q, want %q", upfrontRec.Attributes["offering_class"], "convertible")
	}
	if hourlyRec.Attributes["offering_class"] != "convertible" {
		t.Errorf("hourly offering_class: got %q, want %q", hourlyRec.Attributes["offering_class"], "convertible")
	}

	// Verify pricing amounts.
	if upfrontRec.Price.Amount != "1250.0000000000" {
		t.Errorf("convertible upfront Amount: got %q, want %q", upfrontRec.Price.Amount, "1250.0000000000")
	}
	if hourlyRec.Price.Amount != "0.0760000000" {
		t.Errorf("convertible hourly Amount: got %q, want %q", hourlyRec.Price.Amount, "0.0760000000")
	}

	// Convertible should be distinct from standard offering for same SKU.
	for _, r := range records {
		if r.SKU == "SKU001" && r.PurchaseModel == "reserved" &&
			r.Attributes != nil && r.Attributes["term_code"] == "HU7G6KETJZ" {
			if r.Attributes["offering_class"] == "convertible" {
				t.Error("HU7G6KETJZ should be standard, not convertible")
			}
		}
	}
}

// ---------- Pre-installed software ----------

func TestPreInstalledSoftware(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find the on-demand record for SKU009 (r5.large with SQL Std).
	var rec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU009" && r.PurchaseModel == "on_demand" {
			rec = r
			break
		}
	}
	if rec == nil {
		t.Fatal("did not find on-demand record for SKU009")
	}

	// Verify pre-installed software is in dimensions.
	if rec.Dimensions["pre_installed_sw"] != "SQL Std" {
		t.Errorf("pre_installed_sw: got %q, want %q", rec.Dimensions["pre_installed_sw"], "SQL Std")
	}
	if rec.Dimensions["instance_type"] != "r5.large" {
		t.Errorf("instance_type: got %q, want %q", rec.Dimensions["instance_type"], "r5.large")
	}
	if rec.Price.Amount != "0.8760000000" {
		t.Errorf("Price.Amount: got %q, want %q", rec.Price.Amount, "0.8760000000")
	}
}

// ---------- Previous generation ----------

func TestPreviousGeneration(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find on-demand record for SKU010 (m4.large, previous generation).
	var rec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU010" && r.PurchaseModel == "on_demand" {
			rec = r
			break
		}
	}
	if rec == nil {
		t.Fatal("did not find on-demand record for SKU010")
	}

	if rec.Dimensions["current_generation"] != "No" {
		t.Errorf("current_generation: got %q, want %q", rec.Dimensions["current_generation"], "No")
	}
	if rec.Dimensions["instance_type"] != "m4.large" {
		t.Errorf("instance_type: got %q, want %q", rec.Dimensions["instance_type"], "m4.large")
	}
	if rec.Price.Amount != "0.1000000000" {
		t.Errorf("Price.Amount: got %q, want %q", rec.Price.Amount, "0.1000000000")
	}
}

// ---------- All records validate ----------

func TestAllRecordsValidate(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for i, r := range records {
		if err := catalog.ValidateRecord(r); err != nil {
			t.Errorf("record %d (SKU=%s): validation failed: %v", i, r.SKU, err)
		}
	}
}

// ---------- Expanded fixture record count ----------

func TestExpandedFixtureRecordCount(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Expected 18 records from the expanded fixture:
	//   SKU001 OD: 2 (USD+CNY)
	//   SKU002 OD: 1
	//   SKU006 OD: 1
	//   SKU007 OD: 2 (tiered)
	//   SKU008 OD: 1
	//   SKU009 OD: 1
	//   SKU010 OD: 1
	//   SKU001 RI HU7G6KETJZ: 2 (upfront+hourly)
	//   SKU001 RI 4NA7Y494T4: 1
	//   SKU001 RI 38NPMPTW36: 2 (upfront+$0 hourly)
	//   SKU001 RI CUZHX8X6JH: 2 (upfront+hourly)
	//   SKU002 RI 6QCMYABX3D: 2 (upfront+$0 hourly)
	if len(records) != 18 {
		t.Errorf("expected 18 records, got %d", len(records))
	}
}

// ---------- Tiered dimension in fixture ----------

func TestTieredDimensionInFixture(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find tiered records for SKU007 (t3.micro).
	var tier1, tier2 *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU != "SKU007" {
			continue
		}
		if r.Tier != nil && r.Tier.StartQuantity == "0" && r.Tier.EndQuantity == "100" {
			tier1 = r
		} else if r.Tier != nil && r.Tier.StartQuantity == "100" && r.Tier.EndQuantity == "" {
			tier2 = r
		}
	}

	if tier1 == nil {
		t.Fatal("did not find tier 0-100 for SKU007")
	}
	if tier2 == nil {
		t.Fatal("did not find tier 100-Inf for SKU007")
	}

	if tier1.Price.Amount != "0.0104000000" {
		t.Errorf("Tier1 Amount: got %q, want %q", tier1.Price.Amount, "0.0104000000")
	}
	if tier2.Price.Amount != "0.0052000000" {
		t.Errorf("Tier2 Amount: got %q, want %q", tier2.Price.Amount, "0.0052000000")
	}
	if tier1.Region != "ap-southeast-1" {
		t.Errorf("Tier1 Region: got %q, want %q", tier1.Region, "ap-southeast-1")
	}
	if tier1.Dimensions["instance_type"] != "t3.micro" {
		t.Errorf("Tier1 instance_type: got %q, want %q", tier1.Dimensions["instance_type"], "t3.micro")
	}
}

// ---------- Windows All Upfront Reserved ----------

func TestWindowsAllUpfrontReserved(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Find All Upfront reserved records for SKU002 (term 6QCMYABX3D, Windows).
	var upfrontRec, hourlyRec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU != "SKU002" || r.PurchaseModel != "reserved" {
			continue
		}
		if r.Attributes == nil || r.Attributes["term_code"] != "6QCMYABX3D" {
			continue
		}
		if r.PricingDimension == "upfront_fee" {
			upfrontRec = r
		} else if r.PricingDimension == "usage" {
			hourlyRec = r
		}
	}

	if upfrontRec == nil {
		t.Fatal("did not find Windows All Upfront upfront fee record")
	}
	if hourlyRec == nil {
		t.Fatal("did not find Windows All Upfront hourly record")
	}

	if upfrontRec.Price.Amount != "2500.0000000000" {
		t.Errorf("upfront Amount: got %q, want %q", upfrontRec.Price.Amount, "2500.0000000000")
	}
	if hourlyRec.Price.Amount != "0.0000000000" {
		t.Errorf("hourly Amount: got %q, want %q", hourlyRec.Price.Amount, "0.0000000000")
	}
	if upfrontRec.Region != "eu-west-1" {
		t.Errorf("Region: got %q, want %q", upfrontRec.Region, "eu-west-1")
	}
	if upfrontRec.Attributes["purchase_option"] != "All Upfront" {
		t.Errorf("purchase_option: got %q, want %q", upfrontRec.Attributes["purchase_option"], "All Upfront")
	}
}

// ---------- Error: missing offerTermCode ----------

func TestMissingOfferTermCodeError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "",
						"sku": "SKU001",
						"effectiveDate": "2024-01-01T00:00:00Z",
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
		t.Fatal("expected error for missing offerTermCode")
	}
	if !strings.Contains(err.Error(), "offerTermCode") {
		t.Errorf("error should mention offerTermCode, got: %v", err)
	}
}

// ---------- Error: missing SKU in term ----------

func TestMissingSKUInTermError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "JRTCKXETXF",
						"sku": "",
						"effectiveDate": "2024-01-01T00:00:00Z",
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
		t.Fatal("expected error for missing sku in term")
	}
	if !strings.Contains(err.Error(), "missing sku") {
		t.Errorf("error should mention missing sku, got: %v", err)
	}
}

// ---------- Error: term SKU mismatch ----------

func TestTermSKUMismatchError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
				}
			}
		},
		"terms": {
			"OnDemand": {
				"SKU001": {
					"SKU001.JRTCKXETXF": {
						"offerTermCode": "JRTCKXETXF",
						"sku": "SKU999",
						"effectiveDate": "2024-01-01T00:00:00Z",
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
		t.Fatal("expected error for term SKU mismatch")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should mention SKU mismatch, got: %v", err)
	}
}

// ---------- Error: missing priceDimensions ----------

func TestMissingPriceDimensionsError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
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
						"priceDimensions": {},
						"termAttributes": {}
					}
				}
			}
		}
	}`
	conn := New(WithReader(strings.NewReader(input)))
	_, err := conn.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for missing priceDimensions")
	}
	if !strings.Contains(err.Error(), "priceDimensions") {
		t.Errorf("error should mention priceDimensions, got: %v", err)
	}
}

// ---------- Error: missing rateCode ----------

func TestMissingRateCodeError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
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
								"rateCode": "",
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
		t.Fatal("expected error for missing rateCode")
	}
	if !strings.Contains(err.Error(), "rateCode") {
		t.Errorf("error should mention rateCode, got: %v", err)
	}
}

// ---------- Error: empty pricePerUnit ----------

func TestEmptyPricePerUnitError(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
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
								"pricePerUnit": {}
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
		t.Fatal("expected error for empty pricePerUnit")
	}
	if !strings.Contains(err.Error(), "pricePerUnit") {
		t.Errorf("error should mention pricePerUnit, got: %v", err)
	}
}

// ---------- Error: empty price amount string ----------

func TestEmptyPriceAmountStringError(t *testing.T) {
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
								"pricePerUnit": {"USD": ""}
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
		t.Fatal("expected error for empty price amount string")
	}
}

// ---------- Unknown billing unit ----------

func TestUnknownBillingUnit(t *testing.T) {
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
								"unit": "GB-Mo",
								"beginRange": "0",
								"endRange": "Inf",
								"pricePerUnit": {"USD": "0.0230000000"}
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

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	// Unknown unit should be lowercased, not silently dropped.
	if records[0].BillingUnit != "gb-mo" {
		t.Errorf("BillingUnit: got %q, want %q", records[0].BillingUnit, "gb-mo")
	}
}

// ---------- Unsupported term category skipped ----------

func TestUnsupportedTermCategorySkipped(t *testing.T) {
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
								"pricePerUnit": {"USD": "0.1920000000"}
							}
						},
						"termAttributes": {}
					}
				}
			},
			"SavingsPlan": {
				"SKU001": {
					"SKU001.SAVINGS": {
						"some": "structure"
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

	// Should have exactly the OnDemand record; SavingsPlan should be skipped.
	if len(records) != 1 {
		t.Errorf("expected 1 record (SavingsPlan skipped), got %d", len(records))
	}
}

// ---------- License model preservation ----------

func TestLicenseModelPreservation(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// SKU001 (Linux): "No License required"
	// SKU002 (Windows): "License Included"
	for _, r := range records {
		if r.SKU == "SKU001" && r.PurchaseModel == "on_demand" && r.Price.Currency == "USD" {
			if r.Dimensions["license_model"] != "No License required" {
				t.Errorf("SKU001 license_model: got %q, want %q",
					r.Dimensions["license_model"], "No License required")
			}
		}
		if r.SKU == "SKU002" && r.PurchaseModel == "on_demand" {
			if r.Dimensions["license_model"] != "License Included" {
				t.Errorf("SKU002 license_model: got %q, want %q",
					r.Dimensions["license_model"], "License Included")
			}
		}
	}
}

// ---------- New SKUs record counts ----------

func TestNewSKURecordCounts(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	skuCounts := make(map[string]int)
	for _, r := range records {
		skuCounts[r.SKU]++
	}

	expectations := map[string]int{
		"SKU006": 1, // c5.large OD USD
		"SKU007": 2, // t3.micro OD tiered USD
		"SKU008": 1, // m5.xlarge RHEL OD USD
		"SKU009": 1, // r5.large Windows+SQL OD USD
		"SKU010": 1, // m4.large prev-gen OD USD
	}

	for sku, want := range expectations {
		if got := skuCounts[sku]; got != want {
			t.Errorf("%s record count: got %d, want %d", sku, got, want)
		}
	}
}

// ---------- c5.large on-demand record ----------

func TestC5LargeOnDemand(t *testing.T) {
	conn := newFixtureConnector(t)
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var rec *catalog.PricingRecord
	for i := range records {
		r := &records[i]
		if r.SKU == "SKU006" && r.PurchaseModel == "on_demand" {
			rec = r
			break
		}
	}
	if rec == nil {
		t.Fatal("did not find on-demand record for SKU006 (c5.large)")
	}

	if rec.Dimensions["instance_type"] != "c5.large" {
		t.Errorf("instance_type: got %q, want %q", rec.Dimensions["instance_type"], "c5.large")
	}
	if rec.Region != "us-east-1" {
		t.Errorf("Region: got %q, want %q", rec.Region, "us-east-1")
	}
	if rec.Price.Amount != "0.0850000000" {
		t.Errorf("Price.Amount: got %q, want %q", rec.Price.Amount, "0.0850000000")
	}
	if rec.Dimensions["vcpu"] != "2" {
		t.Errorf("vcpu: got %q, want %q", rec.Dimensions["vcpu"], "2")
	}
	if rec.Dimensions["memory"] != "4 GiB" {
		t.Errorf("memory: got %q, want %q", rec.Dimensions["memory"], "4 GiB")
	}
}

// ---------- Day 4: Generic Pipeline Integration and Hardening Tests ----------

// TestPipelineBuildSnapshot verifies that the EC2 connector integrates cleanly
// with pipeline.BuildSnapshot to produce a valid, deterministically ordered snapshot.
func TestPipelineBuildSnapshot(t *testing.T) {
	conn := newFixtureConnector(t)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	options := pipeline.BuildOptions{
		Version:     "v1.0.0",
		GeneratedAt: now,
	}

	snapshot, err := pipeline.BuildSnapshot(context.Background(), conn, options)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	if snapshot.Version != "v1.0.0" {
		t.Errorf("Version: got %q, want %q", snapshot.Version, "v1.0.0")
	}
	if !snapshot.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt: got %v, want %v", snapshot.GeneratedAt, now)
	}
	if len(snapshot.Records) != 18 {
		t.Fatalf("expected 18 records in snapshot, got %d", len(snapshot.Records))
	}

	// Verify all records validate using canonical schema validator.
	if err := catalog.ValidateRecords(snapshot.Records); err != nil {
		t.Fatalf("ValidateRecords on snapshot output: %v", err)
	}
}

// TestPipelineBuildAndWriteJSONRoundTrip verifies end-to-end writing and parsing of
// snapshots via pipeline.BuildAndWriteJSON and catalog.LoadJSON.
func TestPipelineBuildAndWriteJSONRoundTrip(t *testing.T) {
	conn := newFixtureConnector(t)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	options := pipeline.BuildOptions{
		Version:     "v1.0.0-aws-ec2",
		GeneratedAt: now,
	}

	var buf bytes.Buffer
	if err := pipeline.BuildAndWriteJSON(context.Background(), conn, options, &buf); err != nil {
		t.Fatalf("BuildAndWriteJSON: %v", err)
	}

	loaded, err := catalog.LoadJSON(&buf)
	if err != nil {
		t.Fatalf("catalog.LoadJSON: %v", err)
	}

	if loaded.Version != options.Version {
		t.Errorf("loaded version: got %q, want %q", loaded.Version, options.Version)
	}
	if len(loaded.Records) != 18 {
		t.Errorf("loaded records: got %d, want 18", len(loaded.Records))
	}
}

// TestPipelineEmptyCatalogRejection verifies that when an EC2 connector produces no records,
// the generic pipeline rejects it with pipeline.ErrEmptyCatalog.
func TestPipelineEmptyCatalogRejection(t *testing.T) {
	emptyInput := `{"products": {}, "terms": {}}`
	conn := New(WithReader(strings.NewReader(emptyInput)))
	options := pipeline.BuildOptions{
		Version:     "v1.0.0",
		GeneratedAt: time.Unix(0, 0).UTC(),
	}

	_, err := pipeline.BuildSnapshot(context.Background(), conn, options)
	if err == nil {
		t.Fatal("expected error for empty catalog")
	}
	if !errors.Is(err, pipeline.ErrEmptyCatalog) {
		t.Fatalf("expected pipeline.ErrEmptyCatalog, got: %v", err)
	}
}

// TestPipelineErrorPropagation verifies that connector decoding and source errors
// are propagated through pipeline.BuildSnapshot wrapping connector.SourceError.
func TestPipelineErrorPropagation(t *testing.T) {
	badInput := `{"products": { malformed `
	conn := New(WithReader(strings.NewReader(badInput)))
	options := pipeline.BuildOptions{
		Version:     "v1.0.0",
		GeneratedAt: time.Unix(0, 0).UTC(),
	}

	_, err := pipeline.BuildSnapshot(context.Background(), conn, options)
	if err == nil {
		t.Fatal("expected error for malformed input")
	}

	var sourceErr *connector.SourceError
	if !errors.As(err, &sourceErr) {
		t.Errorf("error should wrap *connector.SourceError, got: %v", err)
	}
}

// TestEvaluationRunnerFactoryCompatibility verifies compatibility with the maintainer
// evaluation runner factory signature func(io.Reader) connector.Connector.
func TestEvaluationRunnerFactoryCompatibility(t *testing.T) {
	factory := runner.Factory(func(r io.Reader) connector.Connector {
		return New(WithReader(r))
	})

	c := factory(bytes.NewReader(loadFixture(t)))
	records, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("factory connector Fetch: %v", err)
	}
	if len(records) != 18 {
		t.Fatalf("expected 18 records from factory-instantiated connector, got %d", len(records))
	}
}

// TestHardenNilHTTPClient verifies that getReader falls back safely when WithHTTPClient(nil) is passed.
func TestHardenNilHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(loadFixture(t))
	}))
	defer server.Close()

	conn := New(WithHTTPClient(nil), WithSourceURL(server.URL))
	records, err := conn.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch with nil HTTPClient option: %v", err)
	}
	if len(records) != 18 {
		t.Errorf("expected 18 records, got %d", len(records))
	}
}

// TestHardenEmptyBillingUnitWhitespace verifies that whitespace-only billing unit returns an error.
func TestHardenEmptyBillingUnitWhitespace(t *testing.T) {
	input := `{
		"products": {
			"SKU001": {
				"sku": "SKU001",
				"productFamily": "Compute Instance",
				"attributes": {
					"instanceType": "m5.xlarge",
					"regionCode": "us-east-1",
					"tenancy": "Shared"
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
								"unit": "   ",
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
		t.Fatal("expected error for whitespace-only billing unit")
	}
	if !strings.Contains(err.Error(), "empty billing unit") {
		t.Errorf("expected empty billing unit error, got: %v", err)
	}
}

// BenchmarkConnectorFetch measures throughput and allocations of parsing the representative fixture.
func BenchmarkConnectorFetch(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "source.json"))
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		conn := New(WithReader(bytes.NewReader(data)))
		records, err := conn.Fetch(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if len(records) != 18 {
			b.Fatalf("expected 18 records, got %d", len(records))
		}
	}
}

// BenchmarkPipelineBuildSnapshot measures throughput and allocations through the entire pipeline.
func BenchmarkPipelineBuildSnapshot(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "source.json"))
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	options := pipeline.BuildOptions{
		Version:     "bench",
		GeneratedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		conn := New(WithReader(bytes.NewReader(data)))
		snapshot, err := pipeline.BuildSnapshot(context.Background(), conn, options)
		if err != nil {
			b.Fatal(err)
		}
		if len(snapshot.Records) != 18 {
			b.Fatalf("expected 18 records, got %d", len(snapshot.Records))
		}
	}
}
