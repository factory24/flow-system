package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDescribeCall(t *testing.T) {
	cases := []struct {
		method, url, want string
	}{
		{"GET", "http://user-service:8080/v1/users/3d4f4032", "user-service GET /v1/users/3d4f4032"},
		{"POST", "http://billing-service:8080/v1/billing/pay", "billing-service POST /v1/billing/pay"},
		{"GET", "https://accounts.1flow.org/realms/flow", "accounts.1flow.org GET /realms/flow"},
		{"GET", "http://zone-service:8080", "zone-service GET /"},
		{"GET", "://not a url", "GET ://not a url"},
	}
	for _, c := range cases {
		if got := describeCall(c.method, c.url); got != c.want {
			t.Errorf("describeCall(%q, %q) = %q, want %q", c.method, c.url, got, c.want)
		}
	}
}

func TestDescribeStatus(t *testing.T) {
	cases := []struct {
		code     int
		contains string
	}{
		{http.StatusUnauthorized, "unauthenticated"},
		{http.StatusForbidden, "permission"},
		{http.StatusNotFound, "could not find"},
		{http.StatusInternalServerError, "internal error"},
		{http.StatusTeapot, "rejected the request"},
	}
	for _, c := range cases {
		if got := describeStatus(c.code); !strings.Contains(got, c.contains) {
			t.Errorf("describeStatus(%d) = %q, want it to contain %q", c.code, got, c.contains)
		}
	}
}

// The error a caller sees must name the service and say what happened, without
// leaking the name of the Go helper that produced it.
func TestDoErrorMessageIsReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"timestamp":1,"message":"","errors":["token signature is invalid"],"data":null}`))
	}))
	defer server.Close()

	c := &Client{HTTP: server.Client()}
	_, err := Do[map[string]any](context.Background(), c, Request{
		Method: http.MethodGet,
		URL:    server.URL + "/v1/users/abc",
	})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	msg := err.Error()
	for _, want := range []string{"GET /v1/users/abc", "HTTP 401", "token signature is invalid"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q is missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "client.Do") {
		t.Errorf("error %q still names the internal helper", msg)
	}
}

func TestDoUnreachableServiceMessage(t *testing.T) {
	// A server that is closed before the call, so the dial is refused immediately.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	c := &Client{HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := Do[map[string]any](context.Background(), c, Request{
		Method: http.MethodGet,
		URL:    url + "/v1/users/abc",
	})
	if err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
	if !strings.Contains(err.Error(), "could not reach the service") {
		t.Errorf("error %q does not say the service was unreachable", err.Error())
	}
}
