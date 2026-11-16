package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

func TestRequireActivePlan(t *testing.T) {
	cases := []struct {
		name, method string
		user         *models.KeycloakUser
		want         int
	}{
		{"lapsed write", http.MethodPost, &models.KeycloakUser{Audience: "vendor", PlanState: "lapsed"}, 403},
		{"lapsed delete", http.MethodDelete, &models.KeycloakUser{Audience: "vendor", PlanState: "lapsed"}, 403},
		{"lapsed read", http.MethodGet, &models.KeycloakUser{Audience: "vendor", PlanState: "lapsed"}, 200},
		{"active write", http.MethodPost, &models.KeycloakUser{Audience: "vendor", PlanState: "active"}, 200},
		{"no claim write", http.MethodPut, &models.KeycloakUser{Audience: "vendor"}, 200},
		{"root bypass", http.MethodPost, &models.KeycloakUser{Audience: "root", PlanState: "lapsed"}, 200},
		{"service bypass", http.MethodPost, &models.KeycloakUser{Role: "service", PlanState: "lapsed"}, 200},
		{"no user", http.MethodPost, nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(tc.method, "/v1/x", nil), httptest.NewRecorder())
			if tc.user != nil {
				c.Set("keycloakUser", tc.user)
			}
			h := RequireActivePlan(func(c echo.Context) error { return c.NoContent(200) })
			_ = h(c)
			if got := c.Response().Status; got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
}
