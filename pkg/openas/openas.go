// Package openas decides whether a platform caller may name a business with X-Business-Id: only while the caller opened that
// business (POST /v1/users/:id/open-as) in the last 8 hours. Service tokens are exempt; every error refuses.
package openas

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/factory24/flow-system/pkg/client"
	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

const (
	Header = "X-Business-Id"
	// Window is how long an open-as keeps the header honoured.
	Window = 8 * time.Hour
	// TTL is how long a positive answer is reused; a refusal is never cached, so a fresh open-as works at once.
	TTL = 60 * time.Second
)

var (
	ErrNotOpened   = errors.New("open the business first: no open-as in the last 8 hours")
	ErrUnavailable = errors.New("open-as could not be checked")
)

// Status maps an error from Verify to an HTTP status: 403 when not opened, 503 when the check could not run.
func Status(err error) int {
	if errors.Is(err, ErrUnavailable) {
		return http.StatusServiceUnavailable
	}
	return http.StatusForbidden
}

type state struct {
	Active    bool      `json:"active"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type key struct{ actor, business string }

type Verifier struct {
	c    *client.Client
	base string
	now  func() time.Time
	mu   sync.Mutex
	ok   map[key]time.Time
}

// New returns a Verifier for the user service at base (".../v1", the CLIENT.USER value).
func New(c *client.Client, base string) *Verifier {
	return &Verifier{c: c, base: strings.TrimRight(base, "/"), now: time.Now, ok: map[key]time.Time{}}
}

// Verify allows business when the caller is not a platform person, names no business, or opened that business recently.
func (v *Verifier) Verify(ctx echo.Context, user *models.KeycloakUser, business string) error {
	if business == "" || user == nil || user.Role == "service" {
		return nil
	}
	k := key{user.ID, business}
	v.mu.Lock()
	until, hit := v.ok[k]
	v.mu.Unlock()
	if hit && v.now().Before(until) {
		return nil
	}
	token := client.Token(ctx)
	if token == "" {
		return ErrNotOpened
	}
	res, err := client.Do[state](client.Context(ctx), v.c, client.Request{
		Method: http.MethodGet, URL: v.base + "/users/open-as/active?businessId=" + url.QueryEscape(business), Token: token,
	})
	if err != nil || res == nil {
		return ErrUnavailable
	}
	if !res.Data.Active {
		return ErrNotOpened
	}
	until = v.now().Add(TTL)
	if !res.Data.ExpiresAt.IsZero() && res.Data.ExpiresAt.Before(until) {
		until = res.Data.ExpiresAt
	}
	v.mu.Lock()
	v.ok[k] = until
	v.mu.Unlock()
	return nil
}

var (
	mu      sync.Mutex
	current *Verifier
)

func shared() *Verifier {
	mu.Lock()
	defer mu.Unlock()
	if current == nil {
		base := os.Getenv("CLIENT.USER")
		if base == "" {
			base = "http://user-service:8080/v1"
		}
		current = New(client.New(nil), base)
	}
	return current
}

// Verify checks with a process-wide Verifier that forwards the caller's own token to the user service (CLIENT.USER).
func Verify(ctx echo.Context, user *models.KeycloakUser, business string) error {
	return shared().Verify(ctx, user, business)
}

// Use replaces the process-wide Verifier; nil restores the default. Tests use it through openastest.
func Use(v *Verifier) {
	mu.Lock()
	current = v
	mu.Unlock()
}
