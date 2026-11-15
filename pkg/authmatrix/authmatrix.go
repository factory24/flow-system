// Package authmatrix drives every route of an Echo app as every tier and compares the status with a table.
package authmatrix

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/factory24/flow-system/pkg/middleware"
	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

// Tier is the caller's audience claim; Anonymous sends no token.
type Tier string

const (
	Anonymous   Tier = ""
	Root        Tier = "root"
	Distributor Tier = "distributor"
	Pharmacy    Tier = "vendor"
	Agent       Tier = "agent"
	Patient     Tier = "user"
)

var Tiers = []Tier{Root, Distributor, Pharmacy, Agent, Patient, Anonymous}

// Allowed means any status except 401 and 403: the caller passed the gate and the handler decided.
const Allowed = -1

const header = "X-Test-Tier"

type stub struct{ middleware.EchoMiddleware }

// Middleware replaces token verification: the X-Test-Tier header becomes the caller's audience, a missing one is 401.
func Middleware(app *echo.Echo) middleware.EchoMiddleware {
	return stub{middleware.New(app)}
}

func (stub) IsAuthorizedJWT(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		tier := c.Request().Header.Get(header)
		if tier == "" {
			return c.NoContent(http.StatusUnauthorized)
		}
		c.Set("keycloakUser", &models.KeycloakUser{ID: "user-" + tier, Audience: tier, BusinessId: "biz-" + tier})
		return next(c)
	}
}

func (s stub) audience(want ...string) func(echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return s.IsAuthorizedJWT(func(c echo.Context) error {
			got := c.Get("keycloakUser").(*models.KeycloakUser).Audience
			for _, w := range want {
				if got == w {
					return next(c)
				}
			}
			return c.NoContent(http.StatusForbidden)
		})
	}
}

func (s stub) IsRoot(n echo.HandlerFunc) echo.HandlerFunc { return s.audience("root")(n) }
func (s stub) IsDistributor(n echo.HandlerFunc) echo.HandlerFunc {
	return s.audience("distributor", "root")(n)
}
func (s stub) IsVendor(n echo.HandlerFunc) echo.HandlerFunc { return s.audience("vendor", "root")(n) }
func (s stub) IsAgent(n echo.HandlerFunc) echo.HandlerFunc  { return s.audience("agent", "root")(n) }
func (s stub) IsUser(n echo.HandlerFunc) echo.HandlerFunc   { return s.audience("user", "root")(n) }
func (s stub) IsAuthorizedPersonas(n echo.HandlerFunc) echo.HandlerFunc {
	return s.IsAuthorizedJWT(n)
}

var param = regexp.MustCompile(`/:[A-Za-z_]+`)

func concrete(path string) string { return param.ReplaceAllString(path, "/x") }

// Case is one route with the status each tier must get. Tiers missing from Want default to Default.
type Case struct {
	Method, Path string
	Body         string
	Want         map[Tier]int
	Default      int
}

func (c Case) want(t Tier) int {
	if w, ok := c.Want[t]; ok {
		return w
	}
	if t == Anonymous {
		return http.StatusUnauthorized
	}
	return c.Default
}

// Run sends every case as every tier.
func Run(t *testing.T, app *echo.Echo, cases []Case) {
	t.Helper()
	for _, c := range cases {
		for _, tier := range Tiers {
			body := strings.NewReader(c.Body)
			req := httptest.NewRequest(c.Method, concrete(c.Path), body)
			req.Header.Set("Content-Type", "application/json")
			if tier != Anonymous {
				req.Header.Set(header, string(tier))
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			want := c.want(tier)
			ok := rec.Code == want
			if want == Allowed {
				ok = rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden
			}
			if !ok {
				t.Errorf("%s %s as %q: got %d, want %d", c.Method, c.Path, tier, rec.Code, want)
			}
		}
	}
}

// Covered fails for every registered route under prefix that no case names, so a new route cannot skip the matrix.
func Covered(t *testing.T, app *echo.Echo, prefix string, cases []Case) {
	t.Helper()
	have := map[string]bool{}
	for _, c := range cases {
		have[c.Method+" "+c.Path] = true
	}
	var missing []string
	for _, r := range app.Routes() {
		if !strings.HasPrefix(r.Path, prefix) || strings.HasSuffix(r.Path, "*") || r.Method == "echo_route_not_found" {
			continue
		}
		if !have[r.Method+" "+r.Path] {
			missing = append(missing, r.Method+" "+r.Path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("routes without a matrix row: %v", missing)
	}
}
