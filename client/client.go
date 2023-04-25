package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/steviebps/realm/utils"
)

type ClientConfig struct {
	Logger  hclog.Logger
	Address string
}

type Client struct {
	logger     hclog.Logger
	address    *url.URL
	config     *ClientConfig
	underlying *http.Client
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

	return &Client{
		address: u,
		config:  c,
		logger:  logger,
		// TODO: add internal client options
		underlying: &http.Client{},
	}, nil
}

func (c *Client) NewRequest(method string, path string, body io.Reader) (*http.Request, error) {
	c.logger.Debug("creating a new request", "method", method, "path", path)
	return http.NewRequest(method, c.address.Scheme+"://"+c.address.Host+"/v1/"+strings.TrimPrefix(path, "/"), body)
}

func (c *Client) Do(r *http.Request) (*http.Response, error) {
	c.logger.Debug("executing request", "method", r.Method, "path", r.URL.Path, "host", r.URL.Host)
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
