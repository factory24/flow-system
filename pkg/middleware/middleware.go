package middleware

import (
	"context"
	"crypto/tls"
	"errors"
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
// oidcDiscoveryRetries/oidcDiscoveryBackoff bound how hard getOIDCProvider
// fights a cold pod racing a not-yet-ready egress path before giving up.
// Prod symptom this exists for: a fresh pod's FIRST authenticated request
// hits Keycloak discovery while the sidecar/route isn't fully up yet, gets
// a bare EOF, and — with no retry — that one unlucky request fails auth
// for the pod's entire lifetime even though a retry a moment later would
// have succeeded (see learns/user-service-keycloak-discovery-eof-401.md).
const (
	oidcDiscoveryRetries = 3
	oidcDiscoveryBackoff = 500 * time.Millisecond
)

// ErrOIDCProviderUnavailable wraps a discovery/JWKS failure so callers can
// tell "Keycloak is unreachable" (infra, retry-able, not the caller's fault)
// apart from "the token failed verification" (a real auth rejection). Callers
// must return 503 for the former and 401 for the latter — collapsing both
// into 401, as this middleware did before, silently logs users out and reads
// as an auth failure for what is actually a dependency outage.
type ErrOIDCProviderUnavailable struct{ Cause error }

func (e *ErrOIDCProviderUnavailable) Error() string {
	return fmt.Sprintf("auth provider temporarily unavailable: %s", e.Cause.Error())
}
func (e *ErrOIDCProviderUnavailable) Unwrap() error { return e.Cause }

// A mutex (not sync.Once) is used deliberately: if Keycloak isn't reachable
// yet on first use, we want the next request to retry, not permanently wedge
// auth for the pod's lifetime.
func (middleware *echoMiddleware) getOIDCProvider(keycloakURL string) (*oidc.Provider, *http.Client, error) {
	middleware.oidcMu.Lock()
	defer middleware.oidcMu.Unlock()

	if middleware.oidcProvider != nil && middleware.oidcURL == keycloakURL {
		return middleware.oidcProvider, middleware.oidcHTTP, nil
	}

	if middleware.oidcHTTP == nil || middleware.oidcURL != keycloakURL {
		tr := &http.Transport{
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			// Without this the pool holds conns open indefinitely; if the LB/
			// proxy in front of Keycloak reaps them first, the next reuse
			// hits a half-closed socket and dies with the same bare EOF this
			// whole retry loop exists to survive.
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		}
		middleware.oidcHTTP = &http.Client{
			Timeout:   time.Duration(60) * time.Second,
			Transport: tr,
		}
	}
	client := middleware.oidcHTTP

	var provider *oidc.Provider
	var err error
	for attempt := 0; attempt <= oidcDiscoveryRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(oidcDiscoveryBackoff * time.Duration(attempt))
		}

		// Discovery must not inherit a request context. The provider — and the
		// JWKS key set hanging off it — is cached for the pod's lifetime, so a
		// caller that disconnects mid-fetch would otherwise decide auth for
		// every later request.
		discoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		c := oidc.ClientContext(discoveryCtx, client)
		provider, err = oidc.NewProvider(c, keycloakURL)
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, nil, &ErrOIDCProviderUnavailable{Cause: err}
	}

	middleware.oidcProvider = provider
	middleware.oidcURL = keycloakURL
	return provider, client, nil
}

// WarmOIDCProvider pre-fetches and caches the OIDC discovery document/JWKS
// for keycloakURL so the pod's first real authenticated request doesn't pay
// (and risk failing) that round trip itself. Call it in a background
// goroutine right after middleware.New(app) in each service's cmd/main.go —
// it retries internally via getOIDCProvider and simply logs on failure, so
// it must never block startup or be treated as fatal. See
// learns/user-service-keycloak-discovery-eof-401.md.
func (middleware *echoMiddleware) WarmOIDCProvider(keycloakURL string) {
	if _, _, err := middleware.getOIDCProvider(keycloakURL); err != nil {
		fmt.Printf("WarmOIDCProvider: initial OIDC warmup failed, first authenticated request will retry: %s\n", err.Error())
	}
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

		provider, client, err := middleware.getOIDCProvider(keycloakURL)
		if err != nil {
			var unavailable *ErrOIDCProviderUnavailable
			if errors.As(err, &unavailable) {
				// Keycloak/discovery is unreachable — this is not a rejected
				// token, it's a dependency outage. Reporting it as 401 here
				// silently logs users out and reads as an auth failure for
				// what is actually infra flakiness (see
				// learns/user-service-keycloak-discovery-eof-401.md).
				return ctx.JSON(http.StatusServiceUnavailable, response.NewErrorResponse(err.Error()))
			}
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse(err.Error()))
		}
		// Verification can trigger a JWKS refetch shared by every in-flight request
		// (go-oidc dedupes them). Binding that to the request context means one
		// client going away returns "context canceled" to all of them — a 401 for a
		// perfectly valid token. Use an independent deadline instead.
		verifyCtx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), client), 15*time.Second)
		defer cancel()
		c := verifyCtx

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

		provider, client, err := middleware.getOIDCProvider(keycloakURL)
		if err != nil {
			var unavailable *ErrOIDCProviderUnavailable
			if errors.As(err, &unavailable) {
				return ctx.JSON(http.StatusServiceUnavailable, response.NewErrorResponse("Auth provider temporarily unavailable"))
			}
			return ctx.JSON(http.StatusUnauthorized, response.NewErrorResponse("Auth provider error"))
		}
		verifyCtx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), client), 15*time.Second)
		defer cancel()
		c := verifyCtx

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
