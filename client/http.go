package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync/atomic"
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
	// Token is an API key sent as a bearer token. Servers with
	// authentication enabled require one.
	Token string
}

type HttpClient struct {
	underlying *http.Client
	// streaming is used for long-lived watch connections and therefore has no
	// overall client timeout (which would sever the stream).
	streaming  *http.Client
	address    *url.URL
	token      string
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	// warnedPlaintext is set once the client has warned that it sends its
	// token unencrypted.
	warnedPlaintext atomic.Bool
}

// userAgent identifies realm's Go client to servers and proxies.
var userAgent = "realm-go/" + moduleVersion()

// moduleVersion returns the version of the realm module built into this
// binary, without its "v", or "dev" when it has none (a build from a
// checkout, say).
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	const module = "github.com/steviebps/realm"
	m := &info.Main
	if m.Path != module {
		m = nil
		for _, dep := range info.Deps {
			if dep.Path == module {
				m = dep
				break
			}
		}
	}
	if m != nil && m.Replace != nil {
		m = m.Replace
	}
	if m == nil || m.Version == "" || m.Version == "(devel)" {
		return "dev"
	}
	return strings.TrimPrefix(m.Version, "v")
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
		// Not traced: a span would stay open for the stream's whole life.
		streaming:  &http.Client{Transport: http.DefaultTransport},
		address:    u,
		token:      c.Token,
		tracer:     tracer,
		propagator: otel.GetTextMapPropagator(),
	}, nil
}

// Watch opens a long-lived server-sent-events stream of the chamber at path.
// The request is not subject to the client's overall timeout, so the stream can
// stay open. The caller must close the returned response body.
func (c *HttpClient) Watch(ctx context.Context, path string) (*http.Response, error) {
	return c.watch(ctx, path, "watch=true")
}

// WatchSubtree opens a long-lived server-sent-events stream of every chamber
// at and below path (GET /v1/chambers/<path>?subtree=true&watch=true; see
// docs/design/subtree.md). Like Watch, it is not subject to the client's
// overall timeout. The caller must close the returned response body.
func (c *HttpClient) WatchSubtree(ctx context.Context, path string) (*http.Response, error) {
	return c.watch(ctx, path, "subtree=true&watch=true")
}

// GetSubtree reads the inherited view of every chamber at and below path
// (GET /v1/chambers/<path>?subtree=true; see docs/design/subtree.md). The
// caller must close the returned response body.
func (c *HttpClient) GetSubtree(ctx context.Context, path string) (*http.Response, error) {
	req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = "subtree=true"
	return c.Do(req)
}

// watch opens a server-sent-events stream of the chamber at path, asking
// with query.
func (c *HttpClient) watch(ctx context.Context, path string, query string) (*http.Response, error) {
	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("path", path).Str("query", query).Msg("opening chamber watch stream")

	target := c.address.Scheme + "://" + c.address.Host + "/v1/chambers/" + strings.TrimPrefix(path, "/") + "?" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.prepare(req)
	c.propagator.Inject(ctx, propagation.HeaderCarrier(req.Header))
	return c.streaming.Do(req)
}

// NewRequest returns a request for the chamber at path.
func (c *HttpClient) NewRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Request, error) {
	return c.NewAPIRequest(ctx, method, "/v1/chambers/"+strings.TrimPrefix(path, "/"), body)
}

// NewAPIRequest returns a request for apiPath, a path on the server such as
// "/v1/keys".
func (c *HttpClient) NewAPIRequest(ctx context.Context, method string, apiPath string, body io.Reader) (*http.Request, error) {
	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("method", method).Str("path", apiPath).Msg("creating a new request")
	return http.NewRequestWithContext(ctx, method, c.address.Scheme+"://"+c.address.Host+"/"+strings.TrimPrefix(apiPath, "/"), body)
}

func (c *HttpClient) Do(r *http.Request) (*http.Response, error) {
	ctx, span := c.tracer.Start(r.Context(), "client Do", trace.WithAttributes(attribute.String("realm.client.path", r.URL.Path), attribute.String("realm.client.method", r.Method), attribute.String("realm.client.host", r.URL.Host)))
	defer span.End()

	logger := logging.Ctx(ctx)
	logger.DebugCtx(ctx).Str("method", r.Method).Str("path", r.URL.Path).Str("host", r.URL.Host).Msg("executing request")

	c.prepare(r)
	c.propagator.Inject(ctx, propagation.HeaderCarrier(r.Header))
	return c.underlying.Do(r)
}

// prepare adds realm's User-Agent and the client's API key to r, unless r
// already carries them. The first time it sends the key unencrypted to a host
// other than this machine, it warns.
func (c *HttpClient) prepare(r *http.Request) {
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", userAgent)
	}
	if c.token == "" || r.Header.Get("Authorization") != "" {
		return
	}
	if r.URL.Scheme == "http" && !isLoopback(r.URL.Hostname()) && c.warnedPlaintext.CompareAndSwap(false, true) {
		ctx := r.Context()
		logging.Ctx(ctx).WarnCtx(ctx).Str("host", r.URL.Host).Msg("sending the realm API key over plain http, where anyone on the network path can read it; use an https address")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
}

// isLoopback reports whether host names this machine.
func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
