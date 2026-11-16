package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

func TestSearchSyncOnly(t *testing.T) {
	worker := &models.KeycloakUser{Role: SearchSyncRole}
	viaRealm := &models.KeycloakUser{}
	viaRealm.RealmAccess.Roles = []string{SearchSyncRole}
	cases := []struct {
		name, method, path string
		user               *models.KeycloakUser
		want               int
	}{
		{"sync route", http.MethodGet, "/v1/orders/scripts/sync-search", worker, 200},
		{"sync route realm role", http.MethodGet, "/v1/orders/scripts/sync-search", viaRealm, 200},
		{"sync route wrong method", http.MethodPost, "/v1/orders/scripts/sync-search", worker, 403},
		{"other read", http.MethodGet, "/v1/orders", worker, 403},
		{"other write", http.MethodDelete, "/v1/orders/1", viaRealm, 403},
		{"search view", http.MethodGet, "/v1/orders/_search/open", worker, 403},
		{"other role", http.MethodGet, "/v1/orders", &models.KeycloakUser{Role: "service"}, 200},
		{"no user", http.MethodGet, "/v1/orders", nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := echo.New().NewContext(httptest.NewRequest(tc.method, tc.path, nil), httptest.NewRecorder())
			if tc.user != nil {
				c.Set("keycloakUser", tc.user)
			}
			_ = SearchSyncOnly(func(c echo.Context) error { return c.NoContent(200) })(c)
			if got := c.Response().Status; got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
}
