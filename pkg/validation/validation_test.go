package validation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

type dto struct {
	Email string `json:"email" validate:"required,email"`
	Qty   int    `json:"quantity" validate:"gt=0"`
}

func TestBadInputReturns400WithFieldNames(t *testing.T) {
	e := echo.New()
	Install(e)
	e.POST("/x", func(c echo.Context) error {
		if _, err := Bind[dto](c); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(rec, req)
		return rec
	}
	rec := post(`{"email":"nope","quantity":0}`)
	if rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct{ Errors []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Errors) != 2 || !strings.HasPrefix(out.Errors[0], "email") || !strings.HasPrefix(out.Errors[1], "quantity") {
		t.Fatalf("errors %v", out.Errors)
	}
	if rec := post(`{"email":"a@b.co","quantity":2}`); rec.Code != 200 {
		t.Fatalf("valid got %d", rec.Code)
	}
	if rec := post(`{`); rec.Code != 400 {
		t.Fatalf("malformed got %d", rec.Code)
	}
}
