package middleware

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/factory24/flow-system/pkg/models"
	"github.com/factory24/flow-system/pkg/response"
	"github.com/getsentry/sentry-go"
	"github.com/labstack/echo/v4"
	echoMw "github.com/labstack/echo/v4/middleware"
)

var (
	AuthorizedPersonas = []string{"vendor", "agent"}
)

// SentryRecoverConfig wires panics recovered by echo's Recover middleware
// through to Sentry. sentry.Init() alone (via config.GetSentryConfig) only
// makes the SDK ready — nothing calls sentry.CaptureException unless
// something in the panic-recovery path does it explicitly. Use it as:
//
//	app.Use(echoMw.RecoverWithConfig(middleware.SentryRecoverConfig()))
//
// instead of plain echoMw.Recover(), which recovers and logs locally but
// never reports anything to Sentry.
//
// This only covers panics inside the HTTP handler chain. A panic in a
// goroutine spawned off a handler (Pulsar consumers, background workers)
// is NOT caught here — see learns/goroutine-panic-recovery.md for that
// pattern (defer recover() + sentry.CaptureException at the goroutine
// boundary itself).
func SentryRecoverConfig() echoMw.RecoverConfig {
	return echoMw.RecoverConfig{
		LogErrorFunc: func(c echo.Context, err error, stack []byte) error {
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetContext("panic", sentry.Context{"stacktrace": string(stack)})
				scope.SetTag("uri", c.Request().RequestURI)
				scope.SetTag("method", c.Request().Method)
				sentry.CaptureException(err)
			})
			return err
		},
	}
}

type EchoMiddleware interface {
	IsAuthorizedJWT(next echo.HandlerFunc) echo.HandlerFunc
	IsAuthorizedPersonas(next echo.HandlerFunc) echo.HandlerFunc
	IsAgent(next echo.HandlerFunc) echo.HandlerFunc
	IsVendor(next echo.HandlerFunc) echo.HandlerFunc
	GetUserClaims(ctx echo.Context) *models.KeycloakUser
	GetAccessToken(ctx echo.Context) string
	IsDistributor(next echo.HandlerFunc) echo.HandlerFunc
	IsRoot(next echo.HandlerFunc) echo.HandlerFunc
	IsUser(next echo.HandlerFunc) echo.HandlerFunc
	IsClientAuthenticated(next echo.HandlerFunc) echo.HandlerFunc
	SystemToken() (string, error)
}

type echoMiddleware struct {
	app *echo.Echo

	oidcMu       sync.Mutex
	oidcHTTP     *http.Client
	oidcProvider *oidc.Provider
	oidcURL      string
}

func New(app *echo.Echo) EchoMiddleware {
	return &echoMiddleware{
		app: app,
	}
}

// getOIDCProvider returns a cached *oidc.Provider (and the http.Client bound
// to it) for keycloakURL, built once and reused across every request.
//
// Previously IsAuthorizedJWT/IsClientAuthenticated built a brand new
// http.Transport (its own connection pool, no reuse) and called
// oidc.NewProvider — which does a discovery fetch AND lazily fetches JWKS —
// on EVERY single authenticated request. Under real traffic that meant a
// fresh TCP handshake to Keycloak (never reusing conns, piling up TIME_WAIT
// sockets) plus a discovery+JWKS round trip per request, for every protected
// route on every service sharing this middleware. That's the extra
// goroutine/socket/memory pressure that showed up as OOMKills and Redis pool
// exhaustion once this got rolled out platform-wide.
//
// A mutex (not sync.Once) is used deliberately: if Keycloak isn't reachable
// yet on first use, we want the next request to retry, not permanently wedge
// auth for the pod's lifetime.
func (middleware *echoMiddleware) getOIDCProvider(ctx context.Context, keycloakURL string) (*oidc.Provider, *http.Client, error) {
	middleware.oidcMu.Lock()
	defer middleware.oidcMu.Unlock()

	if middleware.oidcProvider != nil && middleware.oidcURL == keycloakURL {
		return middleware.oidcProvider, middleware.oidcHTTP, nil
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Timeout:   time.Duration(60) * time.Second,
		Transport: tr,
	}

	c := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(c, keycloakURL)
	if err != nil {
		return nil, nil, err
	}

	middleware.oidcProvider = provider
	middleware.oidcHTTP = client
	middleware.oidcURL = keycloakURL
	return provider, client, nil
}

func (middleware *echoMiddleware) GetUserClaims(ctx echo.Context) *models.KeycloakUser {
	if ctx == nil {
		return nil
	}
	userClaim := ctx.Get("keycloakUser")
	if userClaim == nil {
		return nil
	}

	claims, ok := userClaim.(*models.KeycloakUser)
	if !ok {
		return nil
	}

	if claims.Role != "" {
		claims.IsStaff = true
	}
	return claims
}

func (middleware *echoMiddleware) GetAccessToken(ctx echo.Context) string {
	if ctx == nil || ctx.Request() == nil {
		return ""
	}
	rawAccessToken := ctx.Request().Header.Get("Authorization")
	if rawAccessToken == "" {
		return ""
	}
	return rawAccessToken
}

func (middleware *echoMiddleware) SystemToken() (string, error) {
	token := os.Getenv("SYSTEM_TOKEN")
	if token == "" {
		return "", fmt.Errorf("SYSTEM_TOKEN environment variable not set")
	}
	return token, nil
}

func (middleware *echoMiddleware) IsUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if audience != "user" {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, only 'users' are allowed to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsDistributor(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if audience != "distributor" {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, only 'distributors' are allowed to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsAgent(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if audience != "agent" {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, only 'agents' are allowed to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsRoot(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if audience != "root" {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, only 'root' are allowed to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsVendor(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if audience != "vendor" {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, only 'vendors' are allowed to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsAuthorizedPersonas(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		apiResponse := response.NewApiResponse()
		userClaims := middleware.GetUserClaims(ctx)
		if userClaims == nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		audience := strings.ToLower(userClaims.Audience)

		if !slices.Contains(AuthorizedPersonas, audience) {
			apiResponse.Success = false
			apiResponse.Errors = []string{"sorry, you are not authorized to perform this task"}
			return ctx.JSON(http.StatusForbidden, apiResponse)
		}

		return next(ctx)
	}
}

// IsAuthorizedJWT accepts both human tokens (audience == our own KC.CLIENT_ID)
// and service/system tokens minted via ServiceIdentity (audience is the
// service's own account, not ours). It tries the strict human-audience check
// first; only on failure does it re-verify as a service token (signature +
// issuer only, audience unchecked) so a single middleware covers both cases —
// no separate "any" wrapper needed, and exactly one response is ever written.
func (middleware *echoMiddleware) IsAuthorizedJWT(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		var keycloakURL = fmt.Sprintf("%s/realms/%s", os.Getenv("KC.BASE_URL"), os.Getenv("KC.REALM"))
		var clientID = os.Getenv("KC.CLIENT_ID")

		rawAccessToken := ctx.Request().Header.Get("Authorization")
		if rawAccessToken == "" || !strings.HasPrefix(rawAccessToken, "Bearer ") {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Unauthorized"))
		}
		accessToken := strings.TrimPrefix(rawAccessToken, "Bearer ")

		provider, client, err := middleware.getOIDCProvider(ctx.Request().Context(), keycloakURL)
		if err != nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(err.Error()))
		}
		c := oidc.ClientContext(ctx.Request().Context(), client)

		idToken, humanErr := provider.Verifier(&oidc.Config{ClientID: clientID}).Verify(c, accessToken)
		if humanErr != nil {
			// Not a valid human-audience token — re-verify as a service token
			// (signature/issuer only; service tokens don't carry our clientID
			// as audience).
			var serviceErr error
			idToken, serviceErr = provider.Verifier(&oidc.Config{SkipClientIDCheck: true}).Verify(c, accessToken)
			if serviceErr != nil {
				return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(humanErr.Error()))
			}

			var rawClaims map[string]interface{}
			if err := idToken.Claims(&rawClaims); err != nil {
				return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(err.Error()))
			}

			// Optional: verify azp is in the allowed-service-clients list.
			allowedClients := os.Getenv("KC.ALLOWED_CLIENTS")
			if allowedClients != "" {
				azp, _ := rawClaims["azp"].(string)
				allowedList := strings.Split(allowedClients, ",")
				found := false
				for _, allowed := range allowedList {
					if strings.TrimSpace(allowed) == azp {
						found = true
						break
					}
				}
				if !found {
					return ctx.JSON(http.StatusForbidden, response.NewErrorResponse("service client not authorized"))
				}
			}

			// Service tokens carry the same claim shape as a human user (the
			// ServiceIdentity account is a real Keycloak user), so decode them
			// the same way and just note the source.
			user := new(models.KeycloakUser)
			if err := idToken.Claims(user); err != nil {
				return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(err.Error()))
			}
			if user.Role == "" {
				user.Role = "service"
			}
			ctx.Set("keycloakUser", user)
			return next(ctx)
		}

		user := new(models.KeycloakUser)
		if err := idToken.Claims(user); err != nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(err.Error()))
		}

		ctx.Set("keycloakUser", user)

		return next(ctx)
	}
}

func (middleware *echoMiddleware) IsClientAuthenticated(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		var keycloakURL = fmt.Sprintf("%s/realms/%s", os.Getenv("KC.BASE_URL"), os.Getenv("KC.REALM"))

		rawAccessToken := ctx.Request().Header.Get("Authorization")
		if rawAccessToken == "" || !strings.HasPrefix(rawAccessToken, "Bearer ") {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Partner Authentication Required"))
		}

		provider, client, err := middleware.getOIDCProvider(ctx.Request().Context(), keycloakURL)
		if err != nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Auth provider error"))
		}
		c := oidc.ClientContext(ctx.Request().Context(), client)

		verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
		token := strings.TrimPrefix(rawAccessToken, "Bearer ")
		payload, err := verifier.Verify(c, token)
		if err != nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Invalid Partner Token"))
		}

		var rawClaims map[string]interface{}
		if err := payload.Claims(&rawClaims); err != nil {
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Failed to parse partner claims"))
		}

		businessID, _ := rawClaims["businessId"].(string)
		if businessID == "" {
			return ctx.JSON(http.StatusForbidden, response.NewErrorResponse("Token is missing Business Context"))
		}

		isSandbox := false
		if v, ok := rawClaims["isSandbox"]; ok {
			switch val := v.(type) {
			case bool:
				isSandbox = val
			case string:
				isSandbox = val == "true"
			}
		}

		user := &models.KeycloakUser{
			ID:         payload.Subject,
			BusinessId: businessID,
			Role:       "partner",
		}

		ctx.Set("keycloakUser", user)
		ctx.Set("isSandbox", isSandbox)
		return next(ctx)
	}
}
