package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/factory24/flow-system/pkg/response"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Request describes one inter-service HTTP call for Do.
//
// Auth model — every inter-service call is authenticated automatically:
//
//   - Token set  → use it. This is "context auth": the caller forwards the
//     inbound request's token, e.g. Token: middleware.GetAccessToken(ctx).
//     A missing "Bearer " prefix is added.
//   - Token empty → Do pulls the SYSTEM token from Client.Tokens (the
//     per-service machine identity) and uses that. So you never send an
//     unauthenticated inter-service call: forward the ctx token when you have
//     one, otherwise the service authenticates as itself.
//   - Token empty AND no provider configured → sent unauthenticated (legacy;
//     only when the Client was built without a ServiceTokenProvider).
//
// Set Public: true to force an unauthenticated call even when a provider
// exists (e.g. hitting a genuinely public third-party endpoint via Do).
type Request struct {
	Method string
	URL    string
	// Body is JSON-encoded when non-nil.
	Body any
	// Token is an explicit Authorization value (forwarded context/human JWT).
	// When empty, Do falls back to the system token.
	Token string
	// Public forces an unauthenticated request (skips the system-token fallback).
	Public bool
	// ExpectedStatus is the exact status code required for success.
	// Zero accepts any 2xx.
	ExpectedStatus int
}

// describeCall renders a call the way someone reading a log line or an API
// error needs it — which service was called, and what for:
//
//	user-service GET /v1/users/3d4f4032-45b9-427c-9507-510746b97b6e
//
// The host is the service's own name in-cluster, so it doubles as the label a
// reader recognises. Falls back to the raw URL if it can't be parsed.
func describeCall(method, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("%s %s", method, rawURL)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("%s %s %s", u.Hostname(), method, path)
}

// describeStatus says, in plain words, what the called service did with the
// request. The numeric code is kept for anyone who wants to look it up.
func describeStatus(code int) string {
	switch {
	case code == http.StatusUnauthorized:
		return "rejected the request as unauthenticated (HTTP 401 — the access token was missing, expired, or not accepted)"
	case code == http.StatusForbidden:
		return "refused the request (HTTP 403 — the caller does not have permission)"
	case code == http.StatusNotFound:
		return "could not find what was asked for (HTTP 404)"
	case code == http.StatusConflict:
		return "reported a conflict (HTTP 409 — the resource already exists or has changed)"
	case code == http.StatusRequestTimeout || code == http.StatusGatewayTimeout:
		return fmt.Sprintf("timed out (HTTP %d)", code)
	case code >= 500:
		return fmt.Sprintf("hit an internal error (HTTP %d)", code)
	case code >= 400:
		return fmt.Sprintf("rejected the request (HTTP %d)", code)
	default:
		return fmt.Sprintf("answered with an unexpected status (HTTP %d)", code)
	}
}

// Do performs the request and decodes the body into response.ApiResponse[T].
// All error handling and logging live here so individual clients stay thin:
//
//   - auth failures are returned as errors (never silently sent unauthenticated)
//   - an unexpected status returns an error containing the API's first error
//     message (the partially-decoded ApiResponse is also returned when available)
//   - 204 / empty bodies are not decoded (no spurious EOF)
//
// Errors are phrased for whoever ends up reading them — they surface in service
// logs AND in the `errors` array of the API response the dashboard shows — so
// they name the service being called and what went wrong, rather than the Go
// function that produced them.
func Do[T any](ctx context.Context, c *Client, r Request) (*response.ApiResponse[T], error) {
	call := describeCall(r.Method, r.URL)

	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("%s: not sent — this service has no HTTP client configured", call)
	}

	var bodyReader io.Reader
	if r.Body != nil {
		bodyBytes, err := json.Marshal(r.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: not sent — the request body could not be encoded as JSON: %w", call, err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%s: not sent — the request could not be built: %w", call, err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err := setAuth(ctx, c, r, req); err != nil {
		return nil, fmt.Errorf("%s: not sent — %w", call, err)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: could not reach the service — %w", call, err)
	}
	defer resp.Body.Close()

	ok := resp.StatusCode == r.ExpectedStatus ||
		(r.ExpectedStatus == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300)

	if !ok {
		// otelhttp's own client span already ended by this point (it closes
		// synchronously inside RoundTrip, before Do ever sees the response),
		// and it only reflects transport-level failures anyway — a 4xx/5xx is
		// a "successful" round trip to it. So the API's own error message
		// never reaches the trace at all today. RecordError on the *enclosing*
		// span (the caller's handler span) instead: it's still live, and
		// RecordError adds a non-destructive exception event rather than
		// overwriting span status, so multiple Do calls in one handler don't
		// clobber each other.
		span := trace.SpanFromContext(ctx)

		// Best-effort decode so the caller sees the API's own error message.
		apiResponse := new(response.ApiResponse[T])
		if decodeErr := json.NewDecoder(resp.Body).Decode(apiResponse); decodeErr == nil {
			// ErrorText, not FirstError: a failing response explains itself in
			// `message` as often as in `errors`, and dropping either half is how
			// callers ended up staring at a bare status code.
			apiErrMsg := response.ErrorText(apiResponse, resp.Status)
			err := fmt.Errorf("%s: %s — %s", call, describeStatus(resp.StatusCode), apiErrMsg)
			log.Println(err)
			span.RecordError(err, trace.WithAttributes(
				attribute.String("client.url", r.URL),
				attribute.Int("client.response.status_code", resp.StatusCode),
				attribute.String("client.api_error", apiErrMsg),
			))
			return apiResponse, err
		}
		err := fmt.Errorf("%s: %s", call, describeStatus(resp.StatusCode))
		log.Println(err)
		span.RecordError(err, trace.WithAttributes(
			attribute.String("client.url", r.URL),
			attribute.Int("client.response.status_code", resp.StatusCode),
		))
		return nil, err
	}

	// Nothing to decode on 204 or an explicitly empty body.
	if resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return &response.ApiResponse[T]{Success: true}, nil
	}

	apiResponse := new(response.ApiResponse[T])
	if err := json.NewDecoder(resp.Body).Decode(apiResponse); err != nil {
		return nil, fmt.Errorf("%s: answered with a body this service could not read: %w", call, err)
	}

	return apiResponse, nil
}

// setAuth applies the Request's auth choice to the outgoing request:
// forwarded context token if present, otherwise the system token (so calls are
// authenticated by default). Public:true or a Client with no provider sends none.
func setAuth(ctx context.Context, c *Client, r Request, req *http.Request) error {
	if r.Public {
		return nil
	}

	// Context auth: forward the supplied (human/inbound) token.
	if r.Token != "" {
		token := r.Token
		if !strings.HasPrefix(token, "Bearer ") {
			token = "Bearer " + token
		}
		req.Header.Set("Authorization", token)
		return nil
	}

	// No context token → fall back to this service's own system identity.
	if c.Tokens == nil {
		// No provider wired — legacy unauthenticated call.
		return nil
	}
	token, err := c.Tokens.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("this service could not obtain its own system access token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}
