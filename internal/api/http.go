package api

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPMethod represents HTTP request methods
type HTTPMethod string

const (
	GET     HTTPMethod = "GET"
	POST    HTTPMethod = "POST"
	PUT     HTTPMethod = "PUT"
	PATCH   HTTPMethod = "PATCH"
	DELETE  HTTPMethod = "DELETE"
	HEAD    HTTPMethod = "HEAD"
	OPTIONS HTTPMethod = "OPTIONS"
)

// Request represents an HTTP request
type Request struct {
	Method  HTTPMethod
	URL     string
	Headers map[string]string
	Body    interface{}
	Timeout time.Duration
}

// Response represents an HTTP response
type Response struct {
	StatusCode int
	Status     string
	Headers    map[string][]string
	Body       string
	Time       time.Duration
	Size       int64
}

// Client handles HTTP requests
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new HTTP client
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithCACert creates an HTTP client that trusts an additional CA
// certificate on top of the system pool - for talking to servers using a
// private/self-signed CA (e.g. a local docker-compose stack's shared certs)
// without needing that CA installed system-wide. caCertPath supports a
// leading ~ for the home directory. An empty path behaves exactly like
// NewClient (system trust store only).
func NewClientWithCACert(caCertPath string) (*Client, error) {
	return NewClientWithTLSConfig(caCertPath, "", "")
}

// NewClientWithTLSConfig creates an HTTP client with optional custom CA trust
// and/or client certificate (mutual TLS / mTLS) support:
//   - caCertPath: trusts this CA on top of the system pool (server-side TLS,
//     e.g. a self-signed dev cert)
//   - clientCertPath + clientKeyPath: presents this cert/key pair during the
//     handshake, for servers that respond with "tls: certificate required"
//     (they're demanding a client cert, not just asking to be trusted)
//
// All three paths support a leading ~ for the home directory. All empty
// behaves exactly like NewClient (system trust store, no client cert).
func NewClientWithTLSConfig(caCertPath, clientCertPath, clientKeyPath string) (*Client, error) {
	if caCertPath == "" && clientCertPath == "" && clientKeyPath == "" {
		return NewClient(), nil
	}

	tlsConfig := &tls.Config{}

	if caCertPath != "" {
		resolved, err := expandPath(caCertPath)
		if err != nil {
			return nil, fmt.Errorf("resolving CA cert path %q: %w", caCertPath, err)
		}
		pemBytes, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("reading CA cert %q: %w", resolved, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("no valid PEM certificates found in %q", resolved)
		}
		tlsConfig.RootCAs = pool
	}

	if clientCertPath != "" || clientKeyPath != "" {
		if clientCertPath == "" || clientKeyPath == "" {
			return nil, fmt.Errorf("client cert and client key must both be set (got cert=%q key=%q)", clientCertPath, clientKeyPath)
		}
		resolvedCert, err := expandPath(clientCertPath)
		if err != nil {
			return nil, fmt.Errorf("resolving client cert path %q: %w", clientCertPath, err)
		}
		resolvedKey, err := expandPath(clientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("resolving client key path %q: %w", clientKeyPath, err)
		}
		cert, err := tls.LoadX509KeyPair(resolvedCert, resolvedKey)
		if err != nil {
			return nil, fmt.Errorf("loading client cert/key (%q, %q): %w", resolvedCert, resolvedKey, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

// expandPath resolves a leading ~ (and ~/) to the user's home directory.
func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// Send sends an HTTP request and returns the response
func (c *Client) Send(req *Request) (*Response, error) {
	start := time.Now()

	// Prepare body
	var bodyReader io.Reader
	if req.Body != nil {
		jsonBody, err := json.Marshal(req.Body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewBuffer(jsonBody)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest(string(req.Method), req.URL, bodyReader)
	if err != nil {
		return nil, err
	}

	// Set headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// Set default Content-Type if body exists and not set
	if req.Body != nil && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Send request
	if req.Timeout > 0 {
		c.httpClient.Timeout = req.Timeout
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	// Read response body
	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	elapsed := time.Since(start)

	return &Response{
		StatusCode: httpResp.StatusCode,
		Status:     httpResp.Status,
		Headers:    httpResp.Header,
		Body:       string(bodyBytes),
		Time:       elapsed,
		Size:       int64(len(bodyBytes)),
	}, nil
}

// Collection represents a collection of requests
type Collection struct {
	Name        string
	Description string
	Requests    []*Request
}

// Environment represents environment variables for requests
type Environment struct {
	Name      string
	Variables map[string]string
}
