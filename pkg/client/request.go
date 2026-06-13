package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/factory24/flow-system/pkg/response"
)

// Request describes one inter-service HTTP call for Do.
//
// Auth — set at most ONE of the following (both unset = unauthenticated):
//
//   - Token:  pass an Authorization value directly. For "context auth" the
//     caller forwards the inbound request's token, e.g.
//     Token: middleware.GetAccessToken(ctx). A missing "Bearer " prefix
//     is added automatically.
//   - System: true → Do fetches the service-account token from Client.Tokens
//     (machine identity; for public routes like USSD calling protected
//     endpoints, cron jobs, and workers).
type Request struct {
	Method string
	URL    string
	// Body is JSON-encoded when non-nil.
	Body any
	// Token is an explicit Authorization value (context auth / direct token).
	Token string
	// System selects the service-account token from Client.Tokens.
	System bool
	// ExpectedStatus is the exact status code required for success.
	// Zero accepts any 2xx.
	ExpectedStatus int
}

// Do performs the request and decodes the body into response.ApiResponse[T].
// All error handling and logging live here so individual clients stay thin:
//
//   - auth failures are returned as errors (never silently sent unauthenticated)
//   - an unexpected status returns an error containing the API's first error
//     message (the partially-decoded ApiResponse is also returned when available)
//   - 204 / empty bodies are not decoded (no spurious EOF)
func Do[T any](ctx context.Context, c *Client, r Request) (*response.ApiResponse[T], error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("client.Do %s %s: nil client", r.Method, r.URL)
	}

	var bodyReader io.Reader
	if r.Body != nil {
		bodyBytes, err := json.Marshal(r.Body)
		if err != nil {
			return nil, fmt.Errorf("client.Do %s %s: marshal body: %w", r.Method, r.URL, err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("client.Do %s %s: build request: %w", r.Method, r.URL, err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err := setAuth(ctx, c, r, req); err != nil {
		return nil, err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client.Do %s %s: %w", r.Method, r.URL, err)
	}
	defer resp.Body.Close()

	ok := resp.StatusCode == r.ExpectedStatus ||
		(r.ExpectedStatus == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300)

	if !ok {
		// Best-effort decode so the caller sees the API's own error message.
		apiResponse := new(response.ApiResponse[T])
		if decodeErr := json.NewDecoder(resp.Body).Decode(apiResponse); decodeErr == nil {
			err := fmt.Errorf("client.Do %s %s: status %d: %s",
				r.Method, r.URL, resp.StatusCode, response.FirstError(apiResponse, resp.Status))
			log.Println(err)
			return apiResponse, err
		}
		err := fmt.Errorf("client.Do %s %s: status %d (%s)", r.Method, r.URL, resp.StatusCode, resp.Status)
		log.Println(err)
		return nil, err
	}

	// Nothing to decode on 204 or an explicitly empty body.
	if resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return &response.ApiResponse[T]{Success: true}, nil
	}

	apiResponse := new(response.ApiResponse[T])
	if err := json.NewDecoder(resp.Body).Decode(apiResponse); err != nil {
		return nil, fmt.Errorf("client.Do %s %s: decode response: %w", r.Method, r.URL, err)
	}

	return apiResponse, nil
}

// setAuth applies the Request's auth choice to the outgoing request.
func setAuth(ctx context.Context, c *Client, r Request, req *http.Request) error {
	switch {
	case r.Token != "":
		token := r.Token
		if !strings.HasPrefix(token, "Bearer ") {
			token = "Bearer " + token
		}
		req.Header.Set("Authorization", token)

	case r.System:
		if c.Tokens == nil {
			return fmt.Errorf("client.Do %s %s: system auth requested but client has no token provider", r.Method, r.URL)
		}
		token, err := c.Tokens.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("client.Do %s %s: system auth: %w", r.Method, r.URL, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return nil
}
