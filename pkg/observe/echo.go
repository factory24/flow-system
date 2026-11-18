package observe

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Middleware reads or sets X-Request-Id, puts it on the request context, writes one INFO line per request and records
// request count and latency per route and status. It never logs bodies, headers or query strings.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			id := c.Request().Header.Get(RequestIDHeader)
			if id == "" {
				id = newID()
			}
			c.Response().Header().Set(RequestIDHeader, id)
			c.SetRequest(c.Request().WithContext(WithRequestID(c.Request().Context(), id)))

			err := next(c)
			if err != nil {
				c.Error(err)
			}

			route := c.Path()
			if route == "" {
				route = "unmatched"
			}
			status := c.Response().Status
			d := time.Since(start)
			attrs := []any{"requestId", id, "method", c.Request().Method, "route", route, "status", status, "durationMs", d.Milliseconds()}
			if u, ok := c.Get("keycloakUser").(*models.KeycloakUser); ok && u != nil {
				attrs = append(attrs, "businessId", u.BusinessId, "userId", u.ID)
			}
			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			} else if status >= http.StatusBadRequest {
				level = slog.LevelWarn
			}
			slog.Log(c.Request().Context(), level, "request", attrs...)
			code := strconv.Itoa(status)
			Count("http_requests_total", "method", c.Request().Method, "route", route, "status", code)
			Observe("http_request_duration_seconds", d, "route", route)
			return nil
		}
	}
}

// MetricsHandler serves the metrics in the text exposition format.
func MetricsHandler(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
	c.Response().WriteHeader(http.StatusOK)
	write(c.Response())
	return nil
}

// Register installs the request middleware and GET /metrics on app. /metrics is cluster-internal: the gateway does not route it.
func Register(app *echo.Echo) {
	app.Use(Middleware())
	app.GET("/metrics", MetricsHandler)
}
