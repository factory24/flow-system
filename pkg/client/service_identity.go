package client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ServiceIdentity is a ServiceTokenProvider where each service authenticates
// as its OWN Keycloak user account (username "service.<service-name>", created
// under the root businessId). Every inter-service call then carries the ORIGIN
// service's identity in the JWT (preferred_username = service.<name>), so calls
// can be traced back to the service that started them — the foundation for
// cross-service transaction tracing (Jaeger later).
//
// Flow on first GetToken (then cached until ~expiry):
//  1. password-grant login as service.<name> (login client)
//  2. if the account doesn't exist yet → bootstrap it via the Keycloak admin
//     API (admin client; create user under the root businessId, set a
//     deterministic password) and log in again.
//
// The password is derived as HMAC-SHA256(adminSecret, "flow-service:"+name):
// deterministic across restarts/replicas, never stored anywhere.
//
// ServiceIdentityConfig configures a per-service identity. Two separate
// Keycloak clients are involved (mirroring UserService):
//
//   - ClientID/ClientSecret — the LOGIN client (Direct Access Grants enabled)
//     used for the password-grant login as the service user. May be the same
//     public/confidential client end users log in with (KC.CLIENT_ID/SECRET).
//     A public login client can leave ClientSecret empty.
//   - AdminClientID/AdminClientSecret — the Keycloak admin USER's
//     username/password (NOT a client id/secret), used ONLY to bootstrap
//     (create) the service user via the built-in "admin-cli" client — exactly
//     how UserService authenticates (KC.ADMIN_CLIENT_ID/KC.ADMIN_CLIENT_SECRET).
//     The admin user needs manage-users in the realm. If left empty, bootstrap
//     is disabled and the service user must be pre-seeded.
type ServiceIdentityConfig struct {
	ServiceName       string
	BaseURL           string
	Realm             string
	RootBusinessID    string
	ClientID          string
	ClientSecret      string
	AdminClientID     string
	AdminClientSecret string
	// AccountType set on the bootstrapped service user (claim source for the
	// token's audience). Defaults to "root" — platform-wide system access.
	AccountType string
}

type ServiceIdentity struct {
	cfg ServiceIdentityConfig

	username string
	password string

	mu        sync.Mutex
	cached    string
	expiresAt time.Time

	httpClient *http.Client
}

// NewServiceIdentity builds the per-service identity provider. serviceName
// e.g. "water-credit-service" → Keycloak user "service.water-credit-service".
func NewServiceIdentity(cfg ServiceIdentityConfig) ServiceTokenProvider {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	// Deterministic password derived from the admin secret (stable across
	// restarts/replicas, never stored). Falls back to the login secret if no
	// admin secret is configured.
	seed := cfg.AdminClientSecret
	if seed == "" {
		seed = cfg.ClientSecret
	}
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte("flow-service:" + cfg.ServiceName))

	return &ServiceIdentity{
		cfg:        cfg,
		username:   "service." + cfg.ServiceName,
		password:   hex.EncodeToString(mac.Sum(nil)),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *ServiceIdentity) GetToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != "" && time.Now().Add(30*time.Second).Before(s.expiresAt) {
		return s.cached, nil
	}

	token, expiresIn, err := s.login(ctx)
	if err != nil {
		// Account may not exist yet (first run of this service) — bootstrap it.
		log.Printf("service identity %q: login failed (%v) — bootstrapping account", s.username, err)
		if bootErr := s.ensureAccount(ctx); bootErr != nil {
			return "", fmt.Errorf("service identity %q: bootstrap: %w", s.username, bootErr)
		}
		token, expiresIn, err = s.login(ctx)
		if err != nil {
			return "", fmt.Errorf("service identity %q: login after bootstrap: %w", s.username, err)
		}
	}

	s.cached = token
	s.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	log.Printf("service identity %q: authenticated ✓ (token valid for %ds)", s.username, expiresIn)
	return s.cached, nil
}

// login performs a password-grant login as the service user.
func (s *ServiceIdentity) login(ctx context.Context) (string, int, error) {
	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", s.cfg.ClientID)
	if s.cfg.ClientSecret != "" {
		data.Set("client_secret", s.cfg.ClientSecret)
	}
	data.Set("username", s.username)
	data.Set("password", s.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("login returned %s", resp.Status)
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", 0, err
	}
	return res.AccessToken, res.ExpiresIn, nil
}

// ensureAccount creates (or repairs) the service user via the Keycloak admin
// API using the client's own service-account token, then sets the derived
// password. Idempotent: a 409 on create means the account already exists
// (e.g. password drifted after a secret rotation) and we just reset it.
func (s *ServiceIdentity) ensureAccount(ctx context.Context) error {
	if s.cfg.AdminClientID == "" {
		return fmt.Errorf("no admin client configured — pre-seed the %q user or set AdminClientID/AdminClientSecret", s.username)
	}
	adminToken, err := s.adminToken(ctx)
	if err != nil {
		return fmt.Errorf("admin token: %w", err)
	}

	// The service user must carry the SAME claim data as a normal user so the
	// receiving service treats a system-token call exactly like an authenticated
	// user (no public routes, no special-casing). accountType "root" gives it
	// platform-wide access; businessId scopes it to the root business. These
	// attributes only matter alongside the Keycloak protocol mappers that copy
	// them into the token (audience/businessId/role) — same mappers normal users
	// use. AccountType is overridable via cfg.AccountType.
	accountType := s.cfg.AccountType
	if accountType == "" {
		accountType = "root"
	}
	attributes := map[string][]string{
		"accountType": {accountType},
		"isService":   {"true"},
	}
	if s.cfg.RootBusinessID != "" {
		attributes["businessId"] = []string{s.cfg.RootBusinessID}
	}

	createBody, _ := json.Marshal(map[string]any{
		"username":   s.username,
		"enabled":    true,
		"firstName":  "Service",
		"lastName":   s.cfg.ServiceName,
		"attributes": attributes,
	})

	createURL := fmt.Sprintf("%s/admin/realms/%s/users", s.cfg.BaseURL, s.cfg.Realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(createBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// 201 = created, 409 = already exists (we'll reset the password below).
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("create user returned %s", resp.Status)
	}

	userID, err := s.lookupUserID(ctx, adminToken)
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	passwordBody, _ := json.Marshal(map[string]any{
		"type":      "password",
		"value":     s.password,
		"temporary": false,
	})
	passwordURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password", s.cfg.BaseURL, s.cfg.Realm, userID)
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, passwordURL, bytes.NewReader(passwordBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err = s.httpClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("set password returned %s", resp.Status)
	}

	log.Printf("service identity %q: account ready", s.username)
	return nil
}

func (s *ServiceIdentity) lookupUserID(ctx context.Context, adminToken string) (string, error) {
	lookupURL := fmt.Sprintf("%s/admin/realms/%s/users?username=%s&exact=true",
		s.cfg.BaseURL, s.cfg.Realm, url.QueryEscape(s.username))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("user lookup returned %s", resp.Status)
	}

	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, s.username) {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("user %q not found after create", s.username)
}

// adminToken gets a Keycloak admin token for the bootstrap calls, using the
// SAME mechanism UserService uses (gocloak LoginAdmin): a password grant via
// the built-in public "admin-cli" client, where AdminClientID/AdminClientSecret
// are the admin USER's username/password (i.e. KC.ADMIN_CLIENT_ID /
// KC.ADMIN_CLIENT_SECRET), authenticated against the configured realm.
func (s *ServiceIdentity) adminToken(ctx context.Context) (string, error) {
	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", "admin-cli")
	data.Set("username", s.cfg.AdminClientID)
	data.Set("password", s.cfg.AdminClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("admin login (admin-cli password grant) returned %s", resp.Status)
	}

	var res struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

func (s *ServiceIdentity) tokenURL() string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", s.cfg.BaseURL, s.cfg.Realm)
}
