// Package client is the shared HTTP layer for inter-service calls.
//
// It replaces two things every microservice used to hand-roll in pkg/clients:
//
//  1. the outbound *http.Client with request/response logging (NewHTTP), and
//  2. the per-call boilerplate — build request, set auth header, check the
//     status code, decode ApiResponse[T], extract the first error (Do).
//
// See README.md in this package for usage.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/motemen/go-loghttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewHTTP returns the standard outbound HTTP client: otelhttp wraps loghttp so
// every call gets request/response logging AND an OTel child span with W3C
// traceparent injection (when the request carries an active span context).
//
// otelhttp's default span name is just "HTTP {method}" — every outbound call
// in a trace waterfall looks identical until you expand each one. Name it
// after the target host + path instead so the trace is scannable at a glance.
func NewHTTP() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: otelhttp.NewTransport(&loghttp.Transport{},
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method + " " + r.Host + r.URL.Path
			}),
		),
	}
}

// Client bundles the HTTP client with an optional service-account token
// source. It is what the generic Do helper operates on.
type Client struct {
	HTTP   *http.Client
	Tokens ServiceTokenProvider
}

// New returns a Client for use with Do. tp may be nil if the service never
// makes system-authenticated calls (Request.System would then return an error).
func New(tp ServiceTokenProvider) *Client {
	return &Client{
		HTTP:   NewHTTP(),
		Tokens: tp,
	}
}

// ServiceTokenProvider supplies the machine-identity (service-account) token
// used for system-to-system calls — e.g. a public USSD route in WaterCredit
// calling an authenticated Billing endpoint (F3).
type ServiceTokenProvider interface {
	GetToken(ctx context.Context) (string, error)
}

type keycloakTokenProvider struct {
	clientID     string
	clientSecret string
	tokenURL     string
	cached       string
	expiresAt    time.Time
	mu           sync.Mutex
	httpClient   *http.Client
}

// NewServiceTokenProvider returns a ServiceTokenProvider that fetches a
// client-credentials token from Keycloak and caches it until shortly before
// expiry. The Keycloak client must have "Service accounts" enabled.
func NewServiceTokenProvider(clientID, clientSecret, baseURL, realm string) ServiceTokenProvider {
	return &keycloakTokenProvider{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenURL:     fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", baseURL, realm),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (p *keycloakTokenProvider) GetToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Reuse the cached token while it has at least 30s of life left.
	if p.cached != "" && time.Now().Add(30*time.Second).Before(p.expiresAt) {
		return p.cached, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", p.clientID)
	data.Set("client_secret", p.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak token request failed: %s", resp.Status)
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	p.cached = res.AccessToken
	p.expiresAt = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)

	return p.cached, nil
}
