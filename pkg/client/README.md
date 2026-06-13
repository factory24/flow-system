# flow-system/pkg/client — shared inter-service HTTP client

Every microservice used to copy-paste the same two things into `pkg/clients/`:

1. `func New() *http.Client { return &http.Client{Transport: &loghttp.Transport{}} }`
2. per-call boilerplate: build the request, set the `Authorization` header,
   check the status code, decode `ApiResponse[T]`, pull out the first error.

Both now live here. Services keep a thin `pkg/clients/client.go` wrapper (or
import this package directly) and write **one line per call** instead of ~30.

## The two building blocks

| Function | Returns | Use when |
|---|---|---|
| `client.NewHTTP()` | `*http.Client` (loghttp + 60s timeout) | Legacy clients that still build requests manually and set their own headers. Drop-in replacement for the old local `New()`. |
| `client.New(tp)` | `*client.Client` | Migrated clients using the generic `Do` helper. `tp` may be `nil` if the service never makes system calls. |

## The generic call: `Do[T]`

```go
resp, err := client.Do[*domains.ChargeResponse](ctx.Request().Context(), c, client.Request{
    Method:         http.MethodPost,
    URL:            url,
    Body:           dto,                 // JSON-encoded when non-nil
    ExpectedStatus: http.StatusCreated,  // 0 = accept any 2xx
    System:         true,                // ← auth, see below
})
if err != nil {
    return nil, err
}
return resp.Data, nil
```

`T` is the type of `ApiResponse[T].Data`. Everything else — auth header,
status check, error extraction (`response.FirstError`), 204/empty-body
handling, logging — happens inside `Do`.

### Auth — exactly one of three ways

| Field | Meaning |
|---|---|
| *(neither set)* | Unauthenticated call (public endpoint). |
| `Token: middleware.GetAccessToken(ctx)` | **Context auth** — forward the human JWT from the inbound request. Also accepts any raw token; `"Bearer "` is added if missing. |
| `System: true` | **System auth** — `Do` fetches a machine token from the `ServiceTokenProvider` (Keycloak client-credentials, cached). For public routes that call protected endpoints (USSD → Billing), cron jobs, and workers. Fails loudly if the provider is missing or errors — it never silently sends an unauthenticated request. |

### Status handling

- `ExpectedStatus: 201` → exactly 201 or error.
- `ExpectedStatus: 0` → any 2xx passes.
- On an unexpected status, `Do` best-effort decodes the body so the returned
  error contains the API's own first error message; the partially decoded
  `ApiResponse` is also returned when available.
- `204 No Content` / empty bodies are not decoded (no spurious `EOF`).

## System auth setup (F3)

The provider is wired once in `cmd/main.go`:

```go
tp := client.NewServiceTokenProvider(
    cfg.GetKeycloakClientID(),
    cfg.GetKeycloakClientSecret(),
    cfg.GetKeycloakBaseURL(),
    cfg.GetKeycloakRealm(),
)
billingClient := clients.NewBillingClient(client.New(tp))
```

Prerequisite: the Keycloak client must have **Service accounts enabled** and
the roles needed by the endpoints it calls; tokens are cached and refreshed
30s before expiry.

## When NOT to use `Do`

If a call doesn't fit the `ApiResponse[T]` envelope (streams, file downloads,
non-JSON bodies, exotic auth), write a manual client with `NewHTTP()` — don't
bend `Do` around it.

## Migration playbook (pilot: WaterCredit → Billing for the public USSD route)

1. Replace the service-local `New()` with the `NewHTTP()` / `New(tp)` wrappers.
2. Migrate ONE client (Billing) to `Do` with `System: true`; build + verify the
   USSD recharge path end-to-end in dev.
3. Only then migrate the remaining clients of that service (use
   `Token: middleware.GetAccessToken(ctx)` for the context-auth ones).
4. Repeat per service.
