package middleware

import (
	"net/http"
	"strings"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/labstack/echo/v4"
)

const PlanLapsed = "lapsed"

// RequireActivePlan refuses every write from a business whose claim planState is lapsed; reads, root and service tokens pass.
func RequireActivePlan(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		switch c.Request().Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return next(c)
		}
		user, _ := c.Get("keycloakUser").(*models.KeycloakUser)
		if user == nil || strings.EqualFold(user.Audience, "root") || user.Role == "service" || user.PlanState != PlanLapsed {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, map[string]any{"error": map[string]any{
			"code":    "plan_lapsed",
			"message": "the plan has lapsed; the account is read-only",
		}})
	}
}
