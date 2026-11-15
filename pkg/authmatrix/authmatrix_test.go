package authmatrix

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestMatrixDrivesRoutesAsEveryTier(t *testing.T) {
	app := echo.New()
	mw := Middleware(app)
	ok := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	app.GET("/v1/x/open", ok, mw.IsAuthorizedJWT)
	app.GET("/v1/x/admin", ok, mw.IsRoot)
	cases := []Case{
		{Method: "GET", Path: "/v1/x/open", Default: 200},
		{Method: "GET", Path: "/v1/x/admin", Default: 403, Want: map[Tier]int{Root: 200}},
	}
	Run(t, app, cases)
	Covered(t, app, "/v1/x", cases)
}
