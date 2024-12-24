package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const DefaultClientTimeout = 15 * time.Second

type ClientConfig struct {
	Logger  hclog.Logger
	Address string
	Timeout time.Duration
}

type Client struct {
	underlying *http.Client
	logger     hclog.Logger
	address    *url.URL
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

func NewClient(c *ClientConfig) (*Client, error) {
	if c.Address == "" {
		return nil, errors.New("address must not be empty")
	}
	u, err := utils.ParseURL(c.Address)
	if err != nil {
		return nil, fmt.Errorf("could not parse address %q: %w", c.Address, err)
	}
	logger := c.Logger

	if logger == nil {
		logger = hclog.Default().Named("client")
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultClientTimeout
	}

	tracer := otel.Tracer("github.com/steviebps/realm")

	return &Client{
		underlying: &http.Client{Timeout: c.Timeout},
		address:    u,
		logger:     logger,
		tracer:     tracer,
		propagator: otel.GetTextMapPropagator(),
	}, nil
}

func (c *Client) NewRequest(method string, path string, body io.Reader) (*http.Request, error) {
	c.logger.Debug("creating a new request", "method", method, "path", path)
	return http.NewRequest(method, c.address.Scheme+"://"+c.address.Host+"/v1/chambers/"+strings.TrimPrefix(path, "/"), body)
}

func (c *Client) Do(r *http.Request) (*http.Response, error) {
	c.logger.Debug("executing request", "method", r.Method, "path", r.URL.Path, "host", r.URL.Host)
	ctx, span := c.tracer.Start(r.Context(), "client Do", trace.WithAttributes(attribute.String("realm.client.path", r.URL.Path), attribute.String("realm.client.method", r.Method), attribute.String("realm.client.host", r.URL.Host)))
	defer span.End()

	c.propagator.Inject(ctx, propagation.HeaderCarrier(r.Header))
	return c.underlying.Do(r)
}

func (c *Client) PerformRequest(method string, path string, body io.Reader) (*http.Response, error) {
	c.logger.Debug("performing a new request", "method", method, "path", path)
	req, err := c.NewRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
