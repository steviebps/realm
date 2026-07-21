package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/steviebps/realm/helper/logging"
	"github.com/steviebps/realm/utils"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const DefaultClientTimeout = 15 * time.Second

type HttpClientConfig struct {
	Address string
	Timeout time.Duration
}

type HttpClient struct {
	underlying *http.Client
	// streaming is used for long-lived watch connections and therefore has no
	// overall client timeout (which would sever the stream).
	streaming  *http.Client
	address    *url.URL
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

func NewHttpClient(c *HttpClientConfig) (*HttpClient, error) {
	if c.Address == "" {
		return nil, errors.New("address must not be empty")
	}
	u, err := utils.ParseURL(c.Address)
	if err != nil {
		return nil, fmt.Errorf("could not parse address %q: %w", c.Address, err)
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultClientTimeout
	}

	tracer := otel.Tracer("github.com/steviebps/realm")

	return &HttpClient{
		underlying: &http.Client{Timeout: c.Timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		streaming:  &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
		address:    u,
		tracer:     tracer,
		propagator: otel.GetTextMapPropagator(),
	}, nil
}

// Watch opens a long-lived server-sent-events stream of the chamber at path.
// The request is not subject to the client's overall timeout, so the stream can
// stay open. The caller must close the returned response body.
func (c *HttpClient) Watch(ctx context.Context, path string) (*http.Response, error) {
	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("path", path).Msg("opening chamber watch stream")

	target := c.address.Scheme + "://" + c.address.Host + "/v1/chambers/" + strings.TrimPrefix(path, "/") + "?watch=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
	return c.streaming.Do(req)
}

func (c *HttpClient) NewRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Request, error) {
	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("method", method).Str("path", path).Msg("creating a new request")
	return http.NewRequestWithContext(ctx, method, c.address.Scheme+"://"+c.address.Host+"/v1/chambers/"+strings.TrimPrefix(path, "/"), body)
}

func (c *HttpClient) Do(r *http.Request) (*http.Response, error) {
	ctx, span := c.tracer.Start(r.Context(), "client Do", trace.WithAttributes(attribute.String("realm.client.path", r.URL.Path), attribute.String("realm.client.method", r.Method), attribute.String("realm.client.host", r.URL.Host)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path).Str("host", r.URL.Host).Msg("executing request")

	c.propagator.Inject(ctx, propagation.HeaderCarrier(r.Header))
	return c.underlying.Do(r)
}

func (c *HttpClient) PerformRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Response, error) {
	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("method", method).Str("path", path).Msg("performing a new request")
	req, err := c.NewRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
