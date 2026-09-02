package http

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// This file implements the Red Hat SSO OAuth2 device-authorization-grant
// flow (RFC 8628) used to authenticate the assisted-plugin wizard against
// the real OpenShift Cluster Manager (OCM) / Assisted Service APIs.
//
// The flow, endpoints, and client ID here were validated against the real
// `ocm` CLI in poc/ocp-archive/ocp-engine/docs/credential-lifecycle-analysis.md
// (device-code grant against sso.redhat.com, `ocm-cli` client, pull secret
// via api.openshift.com). poc/ocp-archive/e2e/auth.go is the original
// blocking/CLI-oriented implementation this is adapted from — here the
// polling is a single non-blocking attempt per call so it can be driven by
// the browser polling an HTTP endpoint instead of a CLI blocking loop.
//
// No Red Hat credentials are ever persisted to the FleetShift database:
// tokens live only in the in-memory ocmSessionStore for the lifetime of the
// wizard session, consistent with the "no/limited storage of customer
// credentials" principle in docs/design/authentication.md.

const (
	ocmDeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	ocmSessionTTL          = 30 * time.Minute
)

// oidcDiscoveryDoc holds the subset of an OIDC discovery document needed
// for the device authorization grant.
type oidcDiscoveryDoc struct {
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

// deviceAuthorization is the response from the device authorization
// endpoint (RFC 8628 section 3.2).
type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// ocmTokenResponse is the response from the token endpoint, for both the
// success case (AccessToken populated) and the pending/error case (Error
// populated per RFC 8628 section 3.5, e.g. "authorization_pending").
type ocmTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// discoverOIDC fetches the OIDC discovery document from the issuer's
// .well-known/openid-configuration endpoint.
func discoverOIDC(ctx context.Context, client *http.Client, issuer string) (*oidcDiscoveryDoc, error) {
	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("creating discovery request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	var doc oidcDiscoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decoding discovery document: %w", err)
	}
	if doc.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("issuer %q does not advertise a device_authorization_endpoint", issuer)
	}

	return &doc, nil
}

// generatePKCE creates a PKCE code verifier and S256 challenge. Red Hat SSO
// requires PKCE on the device authorization request.
func generatePKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge
}

// startDeviceAuthorization initiates the device code flow by posting to the
// device authorization endpoint. Returns the device authorization details
// and the PKCE code verifier (needed when polling the token endpoint).
func startDeviceAuthorization(ctx context.Context, client *http.Client, endpoint, clientID, scope string) (*deviceAuthorization, string, error) {
	verifier, challenge := generatePKCE()

	data := url.Values{
		"client_id":             {clientID},
		"scope":                 {scope},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, "", fmt.Errorf("creating device authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("device authorization endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var da deviceAuthorization
	if err := json.NewDecoder(resp.Body).Decode(&da); err != nil {
		return nil, "", fmt.Errorf("decoding device authorization response: %w", err)
	}
	if da.Interval == 0 {
		da.Interval = 5
	}

	return &da, verifier, nil
}

// pollTokenOnce makes a single, non-blocking attempt to exchange the device
// code for a token. The caller inspects TokenResponse.Error to distinguish
// "still waiting" (authorization_pending / slow_down) from terminal
// failure or success (Error == "").
func pollTokenOnce(ctx context.Context, client *http.Client, tokenEndpoint, clientID, deviceCode, codeVerifier string) (*ocmTokenResponse, error) {
	data := url.Values{
		"grant_type":  {ocmDeviceCodeGrantType},
		"client_id":   {clientID},
		"device_code": {deviceCode},
	}
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("polling token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	var token ocmTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}

	return &token, nil
}

// refreshOCMToken exchanges a refresh token for a new access token.
func refreshOCMToken(ctx context.Context, client *http.Client, tokenEndpoint, clientID, refreshToken string) (*ocmTokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refreshing token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh endpoint returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var token ocmTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decoding refresh response: %w", err)
	}

	return &token, nil
}

// ocmSession tracks a single in-flight or completed device-code login. It
// is intentionally ephemeral and in-memory only — never persisted.
type ocmSession struct {
	mu sync.Mutex

	tokenEndpoint string
	clientID      string
	deviceCode    string
	codeVerifier  string

	createdAt     time.Time
	deviceExpiry  time.Time
	pollInterval  int
	authenticated bool
	// terminalStatus is "expired" or "error" once polling has reached a
	// terminal, non-retryable state; empty while still pending. Tracked
	// separately from terminalErr so a terminal "expired" state (no
	// message needed) can't be shadowed by generic error-message checks.
	terminalStatus string
	terminalErr    string
	accessToken    string
	refreshToken   string
	idToken        string
	accessTokenExp time.Time

	// pullSecret caches the Red Hat pull secret fetched via
	// POST /api/accounts_mgmt/v1/access_token, keyed to this session
	// since it doesn't change within a user's Red Hat account. Empty
	// until the first cluster or InfraEnv creation call fetches it.
	pullSecret string
}

// expired reports whether this session should be garbage-collected from
// the store. This is a generous, flat TTL from creation — distinct from
// deviceExpiry, which is the OAuth device code's own short-lived expiry
// and is handled explicitly by HandlePollDeviceAuth so it can report a
// clean "expired" status to the browser rather than the session vanishing
// out from under an in-flight poll.
func (s *ocmSession) expired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().After(s.createdAt.Add(ocmSessionTTL))
}

// ocmSessionStore holds in-memory ocmSessions keyed by an opaque session ID
// handed to the browser. Entries are swept lazily on Create.
type ocmSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*ocmSession
}

func newOCMSessionStore() *ocmSessionStore {
	return &ocmSessionStore{sessions: make(map[string]*ocmSession)}
}

func (s *ocmSessionStore) create(sess *ocmSession) string {
	id := uuid.NewString()

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.sessions {
		if v.expired() {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = sess
	return id
}

func (s *ocmSessionStore) get(id string) (*ocmSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || sess.expired() {
		return nil, false
	}
	return sess, true
}
