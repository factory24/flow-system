package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

func TestRequestLineCarriesTheStandardFieldsAndMetricsCountIt(t *testing.T) {
	var buf bytes.Buffer
	initTo(&buf, "demo-service")
	app := echo.New()
	app.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("keycloakUser", &models.KeycloakUser{ID: "u-1", BusinessId: "b-1"})
			return next(c)
		}
	})
	Register(app)
	app.GET("/v1/items/:id", func(c echo.Context) error {
		if RequestID(c.Request().Context()) != "req-7" {
			t.Fatal("request id not on the context")
		}
		return c.String(http.StatusTeapot, "x")
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/items/42?phone=0240000000", nil)
	req.Header.Set(RequestIDHeader, "req-7")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Header().Get(RequestIDHeader) != "req-7" {
		t.Fatal("request id not returned")
	}

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line is not JSON: %v: %s", err, buf.String())
	}
	for k, want := range map[string]any{"service": "demo-service", "requestId": "req-7", "businessId": "b-1", "userId": "u-1",
		"method": "GET", "route": "/v1/items/:id", "status": float64(418), "level": "WARN"} {
		if line[k] != want {
			t.Fatalf("%s = %v, want %v (%s)", k, line[k], want, buf.String())
		}
	}
	if _, ok := line["durationMs"]; !ok || strings.Contains(buf.String(), "0240000000") {
		t.Fatalf("durationMs missing or the query leaked: %s", buf.String())
	}

	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{`http_requests_total{method="GET",route="/v1/items/:id",status="418"} 1`, `http_request_duration_seconds_count{route="/v1/items/:id"} 1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics lack %q:\n%s", want, body)
		}
	}
}

func TestRequestIDIsGeneratedWhenAbsentAndEmptyWithoutContext(t *testing.T) {
	app := echo.New()
	Register(app)
	app.GET("/x", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if len(rec.Header().Get(RequestIDHeader)) != 32 {
		t.Fatal("no generated request id")
	}
	if RequestID(context.Background()) != "" {
		t.Fatal("empty context must give no id")
	}
}
