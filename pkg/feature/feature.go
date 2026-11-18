// Package feature reads the platform's product switches from SettingsService (`feature.<name>`): platform-wide, with an optional
// per-tenant override. A switch that cannot be read is off.
package feature

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/factory24/flow-system/pkg/client"
)

const (
	prefix = "feature."
	// TTL is how long a read is reused; a change by the platform admin shows within this time.
	TTL = 60 * time.Second
)

type state struct {
	Enabled bool `json:"enabled"`
}

// Reader reads switches through the service's own identity.
type Reader struct {
	c    *client.Client
	base string
}

// New returns a Reader for the settings service at base (".../v1/settings").
func New(c *client.Client, base string) *Reader {
	return &Reader{c: c, base: strings.TrimRight(base, "/")}
}

// Enabled reports whether the switch is on for the tenant (empty tenant: the platform value). Any error reads as off.
func (r *Reader) Enabled(ctx context.Context, name, tenantID string) bool {
	if r == nil || r.c == nil || name == "" {
		return false
	}
	code := strings.TrimPrefix(name, prefix)
	u := r.base + "/features/" + url.PathEscape(prefix+code) + "/state"
	if tenantID != "" {
		u += "?businessId=" + url.QueryEscape(tenantID)
	}
	res, err := client.Do[state](ctx, r.c, client.Request{Method: "GET", URL: u, CacheTTL: TTL})
	if err != nil || res == nil {
		return false
	}
	return res.Data.Enabled
}

var (
	mu      sync.RWMutex
	current *Reader
)

// Use sets the reader the package-level Enabled uses; the settings URL is CLIENT.SETTINGS or the in-cluster default.
func Use(c *client.Client) {
	base := os.Getenv("CLIENT.SETTINGS")
	if base == "" {
		base = "http://settings-service:8080/v1/settings"
	}
	mu.Lock()
	current = New(c, base)
	mu.Unlock()
}

// Enabled reports a switch with the reader set by Use; off when none was set.
func Enabled(ctx context.Context, name, tenantID string) bool {
	mu.RLock()
	r := current
	mu.RUnlock()
	return r.Enabled(ctx, name, tenantID)
}
