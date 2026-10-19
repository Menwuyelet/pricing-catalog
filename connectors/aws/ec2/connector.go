package ec2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"pricing-catalog/pkg/catalog"
	"pricing-catalog/pkg/connector"
)

const (
	connectorName  = "aws-ec2"
	defaultTimeout = 5 * time.Minute
)

type Connector struct {
	httpClient *http.Client
	sourceURL  string
	reader     io.Reader
}

type Option func(*Connector)

func WithHTTPClient(client *http.Client) Option {
	return func(c *Connector) {
		c.httpClient = client
	}
}

func WithSourceURL(url string) Option {
	return func(c *Connector) {
		c.sourceURL = url
	}
}

func WithReader(r io.Reader) Option {
	return func(c *Connector) {
		c.reader = r
	}
}

func New(opts ...Option) *Connector {
	c := &Connector{
		httpClient: &http.Client{Timeout: defaultTimeout},
		sourceURL:  sourceURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Connector) Name() string { return connectorName }

func (c *Connector) Fetch(ctx context.Context) ([]catalog.PricingRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, &connector.SourceError{
			Connector: connectorName,
			Err:       err,
		}
	}

	reader, cleanup, err := c.getReader(ctx)
	if err != nil {
		return nil, &connector.SourceError{
			Connector: connectorName,
			Err:       err,
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	if err := ctx.Err(); err != nil {
		return nil, &connector.SourceError{
			Connector: connectorName,
			Err:       err,
		}
	}

	products, terms, err := decodeOfferFile(ctx, reader)
	if err != nil {
		return nil, &connector.SourceError{
			Connector: connectorName,
			Err:       fmt.Errorf("decode offer file: %w", err),
		}
	}

	records, err := normalizeRecords(products, terms)
	if err != nil {
		return nil, &connector.SourceError{
			Connector: connectorName,
			Err:       fmt.Errorf("normalize records: %w", err),
		}
	}

	return records, nil
}

func (c *Connector) getReader(ctx context.Context) (io.Reader, func(), error) {
	if c.reader != nil {
		return c.reader, nil, nil
	}

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}

	targetURL := c.sourceURL
	if targetURL == "" {
		targetURL = sourceURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch source: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("unexpected HTTP status %d from %s", resp.StatusCode, targetURL)
	}

	cleanup := func() {
		resp.Body.Close()
	}

	return resp.Body, cleanup, nil
}
