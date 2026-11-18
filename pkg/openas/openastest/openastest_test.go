package openastest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/factory24/flow-system/pkg/models"
	"github.com/factory24/flow-system/pkg/openas"
	"github.com/labstack/echo/v4"
)

func TestOpenedAndNotOpened(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	ctx := echo.New().NewContext(req, httptest.NewRecorder())
	root := &models.KeycloakUser{ID: "r", Audience: "root"}
	t.Run("opened", func(t *testing.T) {
		Opened(t)
		if err := openas.Verify(ctx, root, "b"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("not opened", func(t *testing.T) {
		NotOpened(t)
		if err := openas.Verify(ctx, root, "b"); err == nil {
			t.Fatal("must refuse")
		}
	})
}
