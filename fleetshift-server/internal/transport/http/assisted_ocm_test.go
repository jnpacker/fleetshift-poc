package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeRedHatSSO is an in-memory fake of the subset of Red Hat SSO's OIDC
// endpoints used by the device-authorization-grant flow. It lets tests
// script the sequence of token-endpoint responses returned to each poll,
// so tests are hermetic (loopback-only, no real network I/O) and
// deterministic (no sleeping — polling is driven by the test, not a
// timer).
type fakeRedHatSSO struct {
	server *httptest.Server

	deviceCode  string
	userCode    string
	expiresIn   int
	interval    int
	pollResults []map[string]any // consumed in order by successive /token polls
	pollCalls   int

	refreshResult map[string]any
	refreshCalls  int
}

func newFakeRedHatSSO(t *testing.T) *fakeRedHatSSO {
	t.Helper()
	f := &fakeRedHatSSO{
		deviceCode: "test-device-code",
		userCode:   "ABCD-1234",
		expiresIn:  600,
		interval:   1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"token_endpoint":                f.server.URL + "/token",
			"device_authorization_endpoint": f.server.URL + "/device",
		})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing device auth form: %v", err)
		}
		if got := r.Form.Get("code_challenge"); got == "" {
			t.Errorf("expected PKCE code_challenge on device authorization request")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"device_code":               f.deviceCode,
			"user_code":                 f.userCode,
			"verification_uri":          "https://sso.redhat.com/device",
			"verification_uri_complete": "https://sso.redhat.com/device?user_code=" + f.userCode,
			"expires_in":                f.expiresIn,
			"interval":                  f.interval,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing token form: %v", err)
		}

		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshCalls++
			if f.refreshResult == nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
				return
			}
			writeJSON(w, http.StatusOK, f.refreshResult)
			return
		}

		if r.Form.Get("code_verifier") == "" {
			t.Errorf("expected PKCE code_verifier on token poll request")
		}

		idx := f.pollCalls
		f.pollCalls++
		if idx >= len(f.pollResults) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
			return
		}
		writeJSON(w, http.StatusOK, f.pollResults[idx])
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newTestAssistedOCMHandler(issuerURL, assistedURL string) *AssistedOCMHandler {
	h := NewAssistedOCMHandler(issuerURL, "test-client", assistedURL, slog.Default())
	return h
}

func TestHandleStartDeviceAuth(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	h := newTestAssistedOCMHandler(sso.server.URL, "")

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/auth/device", nil)
	rec := httptest.NewRecorder()
	h.HandleStartDeviceAuth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp startDeviceAuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty sessionId")
	}
	if resp.UserCode != sso.userCode {
		t.Errorf("expected userCode %q, got %q", sso.userCode, resp.UserCode)
	}
	if resp.Interval != sso.interval {
		t.Errorf("expected interval %d, got %d", sso.interval, resp.Interval)
	}

	if _, ok := h.sessions.get(resp.SessionID); !ok {
		t.Error("expected session to be stored")
	}
}

func TestHandleStartDeviceAuth_DiscoveryFailure(t *testing.T) {
	// Point at a server with no OIDC discovery document at all.
	badServer := httptest.NewServer(http.NotFoundHandler())
	defer badServer.Close()

	h := newTestAssistedOCMHandler(badServer.URL, "")

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/auth/device", nil)
	rec := httptest.NewRecorder()
	h.HandleStartDeviceAuth(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

// startSession is a test helper that runs HandleStartDeviceAuth and returns
// the resulting sessionId.
func startSession(t *testing.T, h *AssistedOCMHandler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/auth/device", nil)
	rec := httptest.NewRecorder()
	h.HandleStartDeviceAuth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start device auth failed: %d: %s", rec.Code, rec.Body.String())
	}
	var resp startDeviceAuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding start response: %v", err)
	}
	return resp.SessionID
}

func pollSession(t *testing.T, h *AssistedOCMHandler, sessionID string) pollDeviceAuthResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/auth/device/poll/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandlePollDeviceAuth(rec, req)
	var resp pollDeviceAuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding poll response (status %d, body %q): %v", rec.Code, rec.Body.String(), err)
	}
	return resp
}

func TestHandlePollDeviceAuth_PendingThenComplete(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = []map[string]any{
		{"error": "authorization_pending"},
		{"error": "slow_down"},
		{
			"access_token":  "fake-access-token",
			"refresh_token": "fake-refresh-token",
			"id_token":      "fake-id-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		},
	}
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	if got := pollSession(t, h, sessionID); got.Status != "pending" {
		t.Fatalf("poll 1: expected pending, got %+v", got)
	}
	got := pollSession(t, h, sessionID)
	if got.Status != "pending" {
		t.Fatalf("poll 2 (slow_down): expected pending, got %+v", got)
	}
	if got.Interval <= sso.interval {
		t.Errorf("expected interval to increase after slow_down, got %d", got.Interval)
	}
	if got := pollSession(t, h, sessionID); got.Status != "complete" {
		t.Fatalf("poll 3: expected complete, got %+v", got)
	}

	// Subsequent polls after completion should keep reporting complete.
	if got := pollSession(t, h, sessionID); got.Status != "complete" {
		t.Fatalf("poll after completion: expected complete, got %+v", got)
	}
	// The token endpoint should not be re-hit once authenticated.
	if sso.pollCalls != 3 {
		t.Errorf("expected exactly 3 token polls, got %d", sso.pollCalls)
	}
}

func TestHandlePollDeviceAuth_ExpiredToken(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = []map[string]any{
		{"error": "expired_token"},
	}
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	got := pollSession(t, h, sessionID)
	if got.Status != "expired" {
		t.Fatalf("expected expired, got %+v", got)
	}

	// Once terminally expired, further polls should not re-hit the token
	// endpoint and should keep reporting the terminal state.
	got = pollSession(t, h, sessionID)
	if got.Status != "expired" {
		t.Fatalf("expected expired on second poll, got %+v", got)
	}
	if sso.pollCalls != 1 {
		t.Errorf("expected exactly 1 token poll after terminal expiry, got %d", sso.pollCalls)
	}
}

func TestHandlePollDeviceAuth_AccessDenied(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = []map[string]any{
		{"error": "access_denied", "error_description": "user declined"},
	}
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	got := pollSession(t, h, sessionID)
	if got.Status != "error" {
		t.Fatalf("expected error status, got %+v", got)
	}
	if got.Error != "user declined" {
		t.Errorf("expected error_description surfaced, got %q", got.Error)
	}
}

func TestHandlePollDeviceAuth_UnknownSession(t *testing.T) {
	h := newTestAssistedOCMHandler("https://example.invalid", "")
	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/auth/device/poll/does-not-exist", nil)
	req.SetPathValue("sessionId", "does-not-exist")
	rec := httptest.NewRecorder()
	h.HandlePollDeviceAuth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandlePollDeviceAuth_DeviceExpiredBeforeCompletion(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	sess, ok := h.sessions.get(sessionID)
	if !ok {
		t.Fatal("expected session to exist")
	}
	// Force the device code into the past without sleeping.
	sess.mu.Lock()
	sess.deviceExpiry = time.Now().Add(-time.Second)
	sess.mu.Unlock()

	got := pollSession(t, h, sessionID)
	if got.Status != "expired" {
		t.Fatalf("expected expired, got %+v", got)
	}
	if sso.pollCalls != 0 {
		t.Errorf("expected no token endpoint call once device code is expired, got %d calls", sso.pollCalls)
	}
}

// authenticatedTestSession completes a device login against sso and
// returns the resulting session ID, ready for authenticated calls.
func authenticatedTestSession(t *testing.T, h *AssistedOCMHandler, sso *fakeRedHatSSO) string {
	t.Helper()
	sso.pollResults = []map[string]any{
		{
			"access_token":  "fake-access-token",
			"refresh_token": "fake-refresh-token",
			"id_token":      "fake-id-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		},
	}
	sessionID := startSession(t, h)
	if got := pollSession(t, h, sessionID); got.Status != "complete" {
		t.Fatalf("expected login to complete, got %+v", got)
	}
	return sessionID
}

func TestHandleAccount(t *testing.T) {
	sso := newFakeRedHatSSO(t)

	assistedMux := http.NewServeMux()
	assistedMux.HandleFunc("/api/accounts_mgmt/v1/current_account", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fake-access-token" {
			t.Errorf("expected bearer token forwarded, got %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"username":   "jdoe",
			"email":      "jdoe@example.com",
			"first_name": "Jane",
			"last_name":  "Doe",
		})
	})
	assistedServer := httptest.NewServer(assistedMux)
	defer assistedServer.Close()

	h := newTestAssistedOCMHandler(sso.server.URL, assistedServer.URL)
	sessionID := authenticatedTestSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/account/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp ocmAccountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding account response: %v", err)
	}
	if resp.Username != "jdoe" || resp.Email != "jdoe@example.com" {
		t.Errorf("unexpected account response: %+v", resp)
	}
}

func TestHandleAccount_NotAuthenticated(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = nil // never completes
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/account/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleAccount(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleOpenShiftVersions(t *testing.T) {
	sso := newFakeRedHatSSO(t)

	assistedMux := http.NewServeMux()
	assistedMux.HandleFunc("/api/assisted-install/v2/openshift-versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"4.19.10": map[string]any{
				"cpu_architectures": []string{"x86_64"},
				"default":           false,
				"display_name":      "4.19.10",
				"support_level":     "production",
			},
			"4.20.1": map[string]any{
				"cpu_architectures": []string{"x86_64", "arm64"},
				"default":           true,
				"display_name":      "4.20.1",
				"support_level":     "production",
			},
		})
	})
	assistedServer := httptest.NewServer(assistedMux)
	defer assistedServer.Close()

	h := newTestAssistedOCMHandler(sso.server.URL, assistedServer.URL)
	sessionID := authenticatedTestSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/openshift-versions/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleOpenShiftVersions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp openShiftVersionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding versions response: %v", err)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d: %+v", len(resp.Versions), resp.Versions)
	}
	// The default version should be sorted first.
	if !resp.Versions[0].Default || resp.Versions[0].Version != "4.20.1" {
		t.Errorf("expected default version 4.20.1 first, got %+v", resp.Versions[0])
	}
}

func TestHandleOpenShiftVersions_UnknownSession(t *testing.T) {
	h := newTestAssistedOCMHandler("https://example.invalid", "https://example.invalid")
	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/openshift-versions/does-not-exist", nil)
	req.SetPathValue("sessionId", "does-not-exist")
	rec := httptest.NewRecorder()
	h.HandleOpenShiftVersions(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestEnsureFreshAccessToken_RefreshesWhenExpired(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.refreshResult = map[string]any{
		"access_token":  "refreshed-access-token",
		"refresh_token": "refreshed-refresh-token",
		"token_type":    "Bearer",
		"expires_in":    3600,
	}

	assistedMux := http.NewServeMux()
	assistedMux.HandleFunc("/api/accounts_mgmt/v1/current_account", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed-access-token" {
			t.Errorf("expected refreshed bearer token forwarded, got %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]string{"username": "jdoe"})
	})
	assistedServer := httptest.NewServer(assistedMux)
	defer assistedServer.Close()

	h := newTestAssistedOCMHandler(sso.server.URL, assistedServer.URL)
	sessionID := authenticatedTestSession(t, h, sso)

	sess, ok := h.sessions.get(sessionID)
	if !ok {
		t.Fatal("expected session")
	}
	// Force the access token to look expired without sleeping.
	sess.mu.Lock()
	sess.accessTokenExp = time.Now().Add(-time.Minute)
	sess.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/account/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleAccount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if sso.refreshCalls != 1 {
		t.Errorf("expected exactly 1 refresh call, got %d", sso.refreshCalls)
	}
}

func TestEnsureFreshAccessToken_NoRefreshTokenFails(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := authenticatedTestSession(t, h, sso)

	sess, ok := h.sessions.get(sessionID)
	if !ok {
		t.Fatal("expected session")
	}
	sess.mu.Lock()
	sess.accessTokenExp = time.Now().Add(-time.Minute)
	sess.refreshToken = ""
	sess.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/account/"+sessionID, nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleAccount(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when refresh is impossible, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOCMSessionStore_SweepsExpiredSessions(t *testing.T) {
	store := newOCMSessionStore()

	expired := &ocmSession{
		createdAt:    time.Now().Add(-time.Hour),
		deviceExpiry: time.Now().Add(-time.Hour),
	}
	id := store.create(expired)

	// Creating a second session should sweep the first, since it is past
	// its device expiry and never authenticated.
	store.create(&ocmSession{
		createdAt:    time.Now(),
		deviceExpiry: time.Now().Add(time.Minute),
	})

	if _, ok := store.get(id); ok {
		t.Error("expected expired session to be swept")
	}
}

func TestDiscoverOIDC_MissingDeviceEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"token_endpoint": "https://example.invalid/token",
		})
	}))
	defer server.Close()

	_, err := discoverOIDC(t.Context(), http.DefaultClient, server.URL)
	if err == nil {
		t.Fatal("expected error for missing device_authorization_endpoint")
	}
}
