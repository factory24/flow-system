package validation

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

// Validator adapts go-playground/validator to echo.Validator and reports json field names.
type Validator struct{ v *validator.Validate }

func New() *Validator {
	v := validator.New()
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return f.Name
		}
		return name
	})
	return &Validator{v: v}
}

// Validate returns an echo 400 listing every failing field.
func (val *Validator) Validate(i any) error {
	err := val.v.Struct(i)
	if err == nil {
		return nil
	}
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	fields := make([]string, 0, len(ve))
	for _, fe := range ve {
		fields = append(fields, fe.Field()+" is "+fe.Tag())
	}
	return &Error{Fields: fields}
}

// Error is a 400 carrying the failing fields.
type Error struct{ Fields []string }

func (e *Error) Error() string { return strings.Join(e.Fields, "; ") }

// Install sets the validator on e and renders *Error as the standard error body with status 400.
func Install(e *echo.Echo) {
	e.Validator = New()
	prev := e.HTTPErrorHandler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		var ve *Error
		if errors.As(err, &ve) && !c.Response().Committed {
			_ = c.JSON(http.StatusBadRequest, map[string]any{"success": false, "message": ve.Error(), "errors": ve.Fields})
			return
		}
		prev(err, c)
	}
}

// Bind binds then validates the request body into a T.
func Bind[T any](c echo.Context) (*T, error) {
	var in T
	if err := c.Bind(&in); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if err := c.Validate(&in); err != nil {
		return nil, err
	}
	return &in, nil
}
