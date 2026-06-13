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
// under the root businessId). Compared to a shared client-credentials account,
// every inter-service call carries the ORIGIN service's identity in the JWT
// (preferred_username = service.<name>), so calls can be traced back to the
// service that started them — the foundation for cross-service transaction
// tracing (Jaeger later).
//
// Flow on first GetToken (then cached until ~expiry):
//  1. password-grant login as service.<name>
//  2. if the account doesn't exist yet → bootstrap it via the Keycloak admin
//     API (create user under the root businessId, set a deterministic
//     password) and log in again.
//
// The password is derived as HMAC-SHA256(clientSecret, "flow-service:"+name):
// deterministic across restarts/replicas, never stored anywhere.
type ServiceIdentity struct {
	serviceName    string
	clientID       string
	clientSecret   string
	baseURL        string
	realm          string
	rootBusinessID string

	username string
	password string

	mu        sync.Mutex
	cached    string
	expiresAt time.Time

	httpClient *http.Client
}

// NewServiceIdentity builds the per-service identity provider.
// serviceName e.g. "water-credit-service"; rootBusinessID scopes the account
// to the platform root business (may be empty — attribute is then omitted).
// The Keycloak client (clientID/clientSecret) must allow the password grant
// and have service accounts enabled with user-management roles for bootstrap.
func NewServiceIdentity(serviceName, clientID, clientSecret, baseURL, realm, rootBusinessID string) ServiceTokenProvider {
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte("flow-service:" + serviceName))

	return &ServiceIdentity{
		serviceName:    serviceName,
		clientID:       clientID,
		clientSecret:   clientSecret,
		baseURL:        strings.TrimRight(baseURL, "/"),
		realm:          realm,
		rootBusinessID: rootBusinessID,
		username:       "service." + serviceName,
		password:       hex.EncodeToString(mac.Sum(nil)),
		httpClient:     &http.Client{Timeout: 15 * time.Second},
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
	return s.cached, nil
}

// login performs a password-grant login as the service user.
func (s *ServiceIdentity) login(ctx context.Context) (string, int, error) {
	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", s.clientID)
	data.Set("client_secret", s.clientSecret)
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
	adminToken, err := s.adminToken(ctx)
	if err != nil {
		return fmt.Errorf("admin token: %w", err)
	}

	attributes := map[string][]string{
		"accountType": {"service"},
		"isService":   {"true"},
	}
	if s.rootBusinessID != "" {
		attributes["businessId"] = []string{s.rootBusinessID}
	}

	createBody, _ := json.Marshal(map[string]any{
		"username":   s.username,
		"enabled":    true,
		"firstName":  "Service",
		"lastName":   s.serviceName,
		"attributes": attributes,
	})

	createURL := fmt.Sprintf("%s/admin/realms/%s/users", s.baseURL, s.realm)
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
	passwordURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password", s.baseURL, s.realm, userID)
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
		s.baseURL, s.realm, url.QueryEscape(s.username))
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

// adminToken gets a client-credentials token for the bootstrap admin calls.
func (s *ServiceIdentity) adminToken(ctx context.Context) (string, error) {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", s.clientID)
	data.Set("client_secret", s.clientSecret)

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
		return "", fmt.Errorf("client credentials returned %s", resp.Status)
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
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", s.baseURL, s.realm)
}
