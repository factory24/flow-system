// Package observe is the platform logging and metrics standard (docs/platform/OBSERVABILITY.md): JSON log lines with
// service, requestId, businessId, userId, route, status and durationMs, and a cluster-internal GET /metrics.
package observe

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

const RequestIDHeader = "X-Request-Id"

// WithRequestID returns a context carrying the request id so outbound calls and published events can forward it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID is the request id on ctx, or "".
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Init installs a JSON slog handler on stdout as the default logger, with the service name on every line, and returns it.
// Standard-library log output is routed through it at INFO.
func Init(service string) *slog.Logger { return initTo(os.Stdout, service) }

func initTo(w io.Writer, service string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).With("service", service)
	slog.SetDefault(logger)
	return logger
}
