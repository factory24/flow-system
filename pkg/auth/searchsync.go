// Package auth holds token-scope guards shared by every service.
package auth

import (
	"net/http"
	"slices"
	"strings"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

const SearchSyncRole = "search-sync"

const syncSuffix = "/scripts/sync-search"

// SearchSyncOnly rejects with 403 any request carrying role search-sync that is not GET .../scripts/sync-search.
func SearchSyncOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user, _ := c.Get("keycloakUser").(*models.KeycloakUser)
		if user == nil || !hasSearchSync(user) {
			return next(c)
		}
		if c.Request().Method == http.MethodGet && strings.HasSuffix(c.Request().URL.Path, syncSuffix) {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, map[string]any{"error": map[string]any{
			"code":    "forbidden",
			"message": "token scope does not allow this route",
		}})
	}
}

func hasSearchSync(u *models.KeycloakUser) bool {
	return u.Role == SearchSyncRole || slices.Contains(u.RealmAccess.Roles, SearchSyncRole)
}
