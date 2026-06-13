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

### Auth — token-or-system, automatic

Every inter-service call is authenticated by default. You only choose whether
to **forward** an inbound token; if you don't, the client authenticates as
itself.

| `Request` field | Behaviour |
|---|---|
| `Token: middleware.GetAccessToken(ctx)` (or `ctx.Request().Header.Get("Authorization")`) | **Context auth** — forward the inbound human/caller JWT. `"Bearer "` added if missing. |
| `Token` empty | **System auth (automatic)** — `Do` pulls this service's own token from the `ServiceTokenProvider` and uses it. You never send a bare inter-service call. |
| `Public: true` | Force an unauthenticated request (skip the system fallback) — for genuinely public third-party endpoints. |

So a client method typically does:

```go
Token: token(ctx),   // inbound token if present; empty → system token kicks in
```

where `token(ctx)` returns `ctx.Request().Header.Get("Authorization")` (empty
on public/USSD requests). Because the system identity is a per-service Keycloak
**user**, the downstream service sees real user claims either way — the human
when forwarded, or `service.<name>` when the system token is used.

If the `Client` was built without a provider (`client.New(nil)`), an empty
`Token` simply sends no auth header (legacy).

### Status handling

- `ExpectedStatus: 201` → exactly 201 or error.
- `ExpectedStatus: 0` → any 2xx passes.
- On an unexpected status, `Do` best-effort decodes the body so the returned
  error contains the API's own first error message; the partially decoded
  `ApiResponse` is also returned when available.
- `204 No Content` / empty bodies are not decoded (no spurious `EOF`).

## System auth setup (F3) — per-service identity

Each service authenticates as its **own Keycloak user account**, named after
the service and scoped under the root businessId. The account is created
automatically on first use (bootstrap via the Keycloak admin API), so nothing
is provisioned by hand. Because the JWT's `preferred_username` is
`service.<name>`, every inter-service call is traceable to the service that
originated it — the foundation for cross-service transaction tracing.

Wired once in `cmd/main.go` (mirrors how UserService talks to Keycloak):

```go
tp := client.NewServiceIdentity(client.ServiceIdentityConfig{
    ServiceName:    "water-credit-service",          // → user "service.water-credit-service"
    BaseURL:        cfg.GetKeycloakBaseURL(),         // KC.BASE_URL
    Realm:          cfg.GetKeycloakRealm(),           // KC.REALM
    RootBusinessID: os.Getenv("SYSTEM.ROOT_BUSINESS_ID"),
    // Login client — the service user authenticates here (KC.CLIENT_ID/SECRET).
    ClientID:     cfg.GetKeycloakClientID(),
    ClientSecret: cfg.GetKeycloakClientSecret(),
    // Admin USER (username/password) for first-run bootstrap — the SAME creds
    // UserService uses. NOT a client id/secret.
    AdminClientID:     os.Getenv("KC.ADMIN_CLIENT_ID"),
    AdminClientSecret: os.Getenv("KC.ADMIN_CLIENT_SECRET"),
})
billingClient := clients.NewBillingClient(tp)
```

How it works:

- **Login**: password grant as `service.<name>` against the LOGIN client
  (`KC.CLIENT_ID`); tokens cached, refreshed 30s before expiry.
- **Bootstrap** (first run, when login 401s): gets an admin token via the
  built-in `admin-cli` client using the admin user's
  `KC.ADMIN_CLIENT_ID`/`KC.ADMIN_CLIENT_SECRET` (**exactly** gocloak's
  `LoginAdmin` — a password grant, not client-credentials), creates the user
  (`accountType=service`, `isService=true`, `businessId=<root>`), sets the
  password, logs in again. Idempotent — a 409 just resets the password.
  If no admin creds are configured, bootstrap is skipped and the service user
  must be pre-seeded.
- **Password**: `HMAC-SHA256(adminSecret, "flow-service:"+name)` — deterministic
  across restarts/replicas, never stored.

**Keycloak prerequisites**:
- The LOGIN client (`KC.CLIENT_ID`) must have **Direct Access Grants enabled**
  (password grant) so the service user can log in.
- The admin user (`KC.ADMIN_CLIENT_ID`) must have **manage-users** in the realm
  (it already does — UserService creates users with it).

`NewServiceTokenProvider(...)` (plain client-credentials, shared identity) is
still available for cases where a per-service user is not wanted.

## When NOT to use `Do`

If a call doesn't fit the `ApiResponse[T]` envelope (streams, file downloads,
non-JSON bodies, exotic auth), write a manual client with `NewHTTP()` — don't
bend `Do` around it.

## Migration playbook (pilot: WaterCredit → Billing for the public USSD route)

1. **Delete the service-local `pkg/clients/client.go` `New()` wrapper.** Legacy
   clients call `client.NewHTTP()` directly — there is NO per-service HTTP-client
   factory to maintain (that was the whole point of moving it into flow-system).
   Migrated clients use `client.New(tp)` + `Do`.
2. Migrate ONE client (Billing) to `Do` with `System: true`; build + verify the
   USSD recharge path end-to-end in dev.
3. Only then migrate the remaining clients of that service (use
   `Token: middleware.GetAccessToken(ctx)` for the context-auth ones).
4. Repeat per service.
