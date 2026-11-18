// Package openastest lets a service's tests act as root with X-Business-Id without a running user service.
package openastest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/factory24/flow-system/pkg/client"
	"github.com/factory24/flow-system/pkg/openas"
)

// Opened makes every platform X-Business-Id read count as opened until the test ends.
func Opened(t testing.TB) {
	t.Helper()
	Answer(t, `{"success":true,"data":{"active":true}}`)
}

// NotOpened makes every platform X-Business-Id read count as never opened.
func NotOpened(t testing.TB) {
	t.Helper()
	Answer(t, `{"success":true,"data":{"active":false}}`)
}

// Answer serves body as the user service's open-as/active reply.
func Answer(t testing.TB, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	openas.Use(openas.New(client.New(nil), srv.URL+"/v1"))
	t.Cleanup(func() { srv.Close(); openas.Use(nil) })
}
