package openas

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/factory24/flow-system/pkg/client"
	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

func ctxWith(token string) echo.Context {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	return echo.New().NewContext(req, httptest.NewRecorder())
}

func stub(t *testing.T, code int, body string, hits *int32) *Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.URL.Path != "/v1/users/open-as/active" || r.URL.Query().Get("businessId") != "biz1" || r.Header.Get("Authorization") != "Bearer root-token" {
			t.Errorf("unexpected call %s auth=%q", r.URL.String(), r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(client.New(nil), srv.URL+"/v1")
}

var root = &models.KeycloakUser{ID: "root-1", Audience: "root"}

func TestRefusedWithoutARecentOpenAs(t *testing.T) {
	var hits int32
	v := stub(t, 200, `{"success":true,"data":{"active":false}}`, &hits)
	err := v.Verify(ctxWith("Bearer root-token"), root, "biz1")
	if !errors.Is(err, ErrNotOpened) || Status(err) != http.StatusForbidden {
		t.Fatalf("want 403 not opened, got %v", err)
	}
	if err := v.Verify(ctxWith("Bearer root-token"), root, "biz1"); err == nil || hits != 2 {
		t.Fatalf("a refusal is not cached: err=%v hits=%d", err, hits)
	}
}

func TestAllowedAfterOpenAsAndCached(t *testing.T) {
	var hits int32
	v := stub(t, 200, `{"success":true,"data":{"active":true,"expiresAt":"2999-01-01T00:00:00Z"}}`, &hits)
	for i := 0; i < 3; i++ {
		if err := v.Verify(ctxWith("Bearer root-token"), root, "biz1"); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Fatalf("one call inside the TTL, got %d", hits)
	}
	v.now = func() time.Time { return time.Now().Add(2 * TTL) }
	if err := v.Verify(ctxWith("Bearer root-token"), root, "biz1"); err != nil || hits != 2 {
		t.Fatalf("asks again after the TTL: err=%v hits=%d", err, hits)
	}
}

func TestPositiveAnswerNeverOutlivesTheWindow(t *testing.T) {
	var hits int32
	soon := time.Now().Add(5 * time.Second).UTC().Format(time.RFC3339Nano)
	v := stub(t, 200, `{"data":{"active":true,"expiresAt":"`+soon+`"}}`, &hits)
	if err := v.Verify(ctxWith("Bearer root-token"), root, "biz1"); err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return time.Now().Add(10 * time.Second) }
	_ = v.Verify(ctxWith("Bearer root-token"), root, "biz1")
	if hits != 2 {
		t.Fatalf("cache must stop at expiresAt, hits=%d", hits)
	}
}

func TestExemptCallers(t *testing.T) {
	var hits int32
	v := stub(t, 200, `{"data":{"active":false}}`, &hits)
	for _, tc := range []struct {
		user     *models.KeycloakUser
		business string
	}{{&models.KeycloakUser{ID: "s", Role: "service"}, "biz1"}, {root, ""}, {nil, "biz1"}} {
		if err := v.Verify(ctxWith("Bearer x"), tc.user, tc.business); err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	if hits != 0 {
		t.Fatalf("exempt callers make no call, hits=%d", hits)
	}
}

func TestFailsClosed(t *testing.T) {
	var hits int32
	v := stub(t, 500, `{"message":"boom"}`, &hits)
	err := v.Verify(ctxWith("Bearer root-token"), root, "biz1")
	if !errors.Is(err, ErrUnavailable) || Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("a 500 refuses: %v", err)
	}
	if err := New(client.New(nil), "http://127.0.0.1:1").Verify(ctxWith("Bearer root-token"), root, "biz1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unreachable refuses: %v", err)
	}
	if err := v.Verify(ctxWith(""), root, "biz1"); !errors.Is(err, ErrNotOpened) {
		t.Fatalf("no token refuses: %v", err)
	}
}
