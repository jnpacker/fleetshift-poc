package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// AssistedOCMHandler proxies the assisted-plugin wizard's Red Hat OCM
// (OpenShift Cluster Manager) integration: a device-authorization-grant
// login against Red Hat SSO, followed by real, authenticated calls to the
// Assisted Service v2 API and OCM accounts management API to populate the
// wizard (e.g. supported OpenShift versions) and confirm the signed-in
// account.
//
// The browser never talks to Red Hat directly — sso.redhat.com and
// api.openshift.com are not known to support CORS for browser-originated
// requests (the same constraint documented for GitHub in
// github_keys.go), so every call is proxied through these handlers.
// Tokens are held only in the in-memory ocmSessionStore, never persisted.
//
// See poc/ocp-archive/ocp-engine/docs/credential-lifecycle-analysis.md for
// the design analysis and validated endpoint/client-ID details this is
// based on.
type AssistedOCMHandler struct {
	// Issuer is the Red Hat SSO OIDC issuer, e.g.
	// "https://sso.redhat.com/auth/realms/redhat-external".
	Issuer string
	// ClientID is the OAuth2 client ID used for the device flow. Defaults
	// to "ocm-cli" (the same public client the `ocm` CLI uses), since
	// FleetShift does not yet have its own registered Red Hat SSO client.
	ClientID string
	// Scope is the OAuth2 scope requested. Red Hat SSO only requires
	// "openid".
	Scope string
	// AssistedServiceBaseURL is the base URL for the Assisted Service /
	// OCM accounts management APIs, e.g. "https://api.openshift.com".
	AssistedServiceBaseURL string
	// HTTPClient is used for all outbound calls. Defaults to a client
	// with a 30s timeout.
	HTTPClient *http.Client
	Logger     *slog.Logger

	sessions *ocmSessionStore
}

// NewAssistedOCMHandler constructs an AssistedOCMHandler with defaults
// filled in and its session store initialized.
func NewAssistedOCMHandler(issuer, clientID, assistedServiceBaseURL string, logger *slog.Logger) *AssistedOCMHandler {
	if clientID == "" {
		clientID = "ocm-cli"
	}
	if issuer == "" {
		issuer = "https://sso.redhat.com/auth/realms/redhat-external"
	}
	if assistedServiceBaseURL == "" {
		assistedServiceBaseURL = "https://api.openshift.com"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AssistedOCMHandler{
		Issuer:                 issuer,
		ClientID:               clientID,
		Scope:                  "openid",
		AssistedServiceBaseURL: assistedServiceBaseURL,
		HTTPClient:             &http.Client{Timeout: 30 * time.Second},
		Logger:                 logger,
		sessions:               newOCMSessionStore(),
	}
}

type startDeviceAuthResponse struct {
	SessionID               string `json:"sessionId"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	UserCode                string `json:"userCode"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// HandleStartDeviceAuth begins a Red Hat SSO device-authorization-grant
// login. Route: POST /api/ui/assisted/auth/device
func (h *AssistedOCMHandler) HandleStartDeviceAuth(w http.ResponseWriter, r *http.Request) {
	doc, err := discoverOIDC(r.Context(), h.HTTPClient, h.Issuer)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "OIDC discovery failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to reach Red Hat SSO"})
		return
	}

	da, verifier, err := startDeviceAuthorization(r.Context(), h.HTTPClient, doc.DeviceAuthorizationEndpoint, h.ClientID, h.Scope)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "device authorization failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to start Red Hat SSO device login"})
		return
	}

	sess := &ocmSession{
		tokenEndpoint: doc.TokenEndpoint,
		clientID:      h.ClientID,
		deviceCode:    da.DeviceCode,
		codeVerifier:  verifier,
		createdAt:     time.Now(),
		deviceExpiry:  time.Now().Add(time.Duration(da.ExpiresIn) * time.Second),
		pollInterval:  da.Interval,
	}
	sessionID := h.sessions.create(sess)

	writeJSON(w, http.StatusOK, startDeviceAuthResponse{
		SessionID:               sessionID,
		VerificationURI:         da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete,
		UserCode:                da.UserCode,
		ExpiresIn:               da.ExpiresIn,
		Interval:                da.Interval,
	})
}

type pollDeviceAuthResponse struct {
	Status   string `json:"status"` // "pending" | "complete" | "expired" | "error" | "not_found"
	Interval int    `json:"interval,omitempty"`
	Error    string `json:"error,omitempty"`
}

// HandlePollDeviceAuth makes a single non-blocking attempt to complete a
// pending device login. The browser is expected to call this on the
// interval returned by HandleStartDeviceAuth until status is anything
// other than "pending".
// Route: GET /api/ui/assisted/auth/device/poll/{sessionId}
func (h *AssistedOCMHandler) HandlePollDeviceAuth(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	sess, ok := h.sessions.get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, pollDeviceAuthResponse{Status: "not_found"})
		return
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.authenticated {
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "complete"})
		return
	}
	if sess.terminalStatus != "" {
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: sess.terminalStatus, Error: sess.terminalErr})
		return
	}
	if time.Now().After(sess.deviceExpiry) {
		sess.terminalStatus = "expired"
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "expired"})
		return
	}

	token, err := pollTokenOnce(r.Context(), h.HTTPClient, sess.tokenEndpoint, sess.clientID, sess.deviceCode, sess.codeVerifier)
	if err != nil {
		// Transient network error — report pending so the browser keeps
		// polling rather than surfacing a hard failure for a blip.
		h.Logger.WarnContext(r.Context(), "device token poll failed", "error", err)
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "pending", Interval: sess.pollInterval})
		return
	}

	switch token.Error {
	case "authorization_pending":
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "pending", Interval: sess.pollInterval})
	case "slow_down":
		sess.pollInterval += 5
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "pending", Interval: sess.pollInterval})
	case "expired_token":
		sess.terminalStatus = "expired"
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "expired"})
	case "":
		sess.authenticated = true
		sess.accessToken = token.AccessToken
		sess.refreshToken = token.RefreshToken
		sess.idToken = token.IDToken
		sess.accessTokenExp = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "complete"})
	default:
		msg := token.Error
		if token.ErrorDescription != "" {
			msg = token.ErrorDescription
		}
		sess.terminalStatus = "error"
		sess.terminalErr = msg
		writeJSON(w, http.StatusOK, pollDeviceAuthResponse{Status: "error", Error: msg})
	}
}

// ensureFreshAccessToken refreshes the session's access token if it has
// expired (or is about to), using the stored refresh token. Must be called
// with sess.mu held.
func (h *AssistedOCMHandler) ensureFreshAccessToken(ctx context.Context, sess *ocmSession) error {
	if time.Now().Before(sess.accessTokenExp.Add(-10 * time.Second)) {
		return nil
	}
	if sess.refreshToken == "" {
		return fmt.Errorf("access token expired and no refresh token available")
	}

	token, err := refreshOCMToken(ctx, h.HTTPClient, sess.tokenEndpoint, sess.clientID, sess.refreshToken)
	if err != nil {
		return fmt.Errorf("refreshing Red Hat SSO token: %w", err)
	}

	sess.accessToken = token.AccessToken
	if token.RefreshToken != "" {
		sess.refreshToken = token.RefreshToken
	}
	sess.accessTokenExp = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return nil
}

// authenticatedSession resolves an authenticated session by ID, refreshing
// its access token if necessary. Returns nil and writes an error response
// if the session is missing or not yet authenticated.
func (h *AssistedOCMHandler) authenticatedSession(w http.ResponseWriter, r *http.Request, sessionID string) *ocmSession {
	sess, ok := h.sessions.get(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired session"})
		return nil
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.authenticated {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Red Hat SSO login not complete"})
		return nil
	}
	if err := h.ensureFreshAccessToken(r.Context(), sess); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to refresh Red Hat SSO token", "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Red Hat SSO session expired, please sign in again"})
		return nil
	}
	return sess
}

type ocmAccountResponse struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

// HandleAccount fetches the signed-in user's OCM account, mainly so the
// wizard can display "Signed in as <username>" confirmation.
// Route: GET /api/ui/assisted/account/{sessionId}
func (h *AssistedOCMHandler) HandleAccount(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	var account struct {
		Username  string `json:"username"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := h.getJSON(r.Context(), accessToken, h.AssistedServiceBaseURL+"/api/accounts_mgmt/v1/current_account", &account); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch OCM account", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch Red Hat account details"})
		return
	}

	writeJSON(w, http.StatusOK, ocmAccountResponse{Username: account.Username, Email: account.Email})
}

// assistedOpenShiftVersion mirrors the relevant subset of the Assisted
// Service v2 "OpenshiftVersion" object.
// https://api.openshift.com/api/assisted-install/v2/openshift-versions
type assistedOpenShiftVersion struct {
	CPUArchitectures []string `json:"cpu_architectures"`
	Default          bool     `json:"default"`
	DisplayName      string   `json:"display_name"`
	SupportLevel     string   `json:"support_level"`
}

type openShiftVersionOption struct {
	Version          string   `json:"version"`
	DisplayName      string   `json:"displayName"`
	CPUArchitectures []string `json:"cpuArchitectures"`
	SupportLevel     string   `json:"supportLevel"`
	Default          bool     `json:"default"`
}

type openShiftVersionsResponse struct {
	Versions []openShiftVersionOption `json:"versions"`
}

// HandleOpenShiftVersions fetches the real list of OpenShift versions
// supported by the Assisted Service, to populate the wizard's OpenShift
// version dropdown. Route: GET /api/ui/assisted/openshift-versions/{sessionId}
func (h *AssistedOCMHandler) HandleOpenShiftVersions(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	var versions map[string]assistedOpenShiftVersion
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/openshift-versions"
	if err := h.getJSON(r.Context(), accessToken, url, &versions); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch OpenShift versions", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch supported OpenShift versions"})
		return
	}

	out := make([]openShiftVersionOption, 0, len(versions))
	for version, v := range versions {
		out = append(out, openShiftVersionOption{
			Version:          version,
			DisplayName:      v.DisplayName,
			CPUArchitectures: v.CPUArchitectures,
			SupportLevel:     v.SupportLevel,
			Default:          v.Default,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Version > out[j].Version
	})

	writeJSON(w, http.StatusOK, openShiftVersionsResponse{Versions: out})
}

// getJSON performs an authenticated GET against the Assisted Service /
// accounts management APIs and decodes the JSON response into v.
func (h *AssistedOCMHandler) getJSON(ctx context.Context, accessToken, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("performing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
