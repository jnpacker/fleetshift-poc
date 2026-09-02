package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAssistedService is an in-memory fake of the subset of the
// Assisted Service v2 API (plus the OCM accounts_mgmt pull secret
// endpoint) used by the cluster-provisioning proxy handlers. Requests
// are recorded so tests can assert on what was actually sent upstream
// (e.g. that the pull secret was merged in, or only fetched once).
type fakeAssistedService struct {
	server *httptest.Server

	pullSecretCalls int
	pullSecretBody  string // JSON body returned for the pull secret fetch

	createClusterCalls int
	createdClusterID   string
	clusterCreateFail  int // if non-zero, respond with this status instead

	createInfraEnvCalls int
	createdInfraEnvID   string

	imageURLCalls int
	imageURL      string

	hostsCalls int
	hostsBody  string // raw JSON array body

	getClusterCalls  int
	getClusterStatus string

	lastCreateClusterReq  map[string]any
	lastCreateInfraEnvReq map[string]any
}

func newFakeAssistedService(t *testing.T) *fakeAssistedService {
	t.Helper()
	f := &fakeAssistedService{
		pullSecretBody:    `{"auths":{"quay.io":{"auth":"fake"}}}`,
		createdClusterID:  "cluster-123",
		createdInfraEnvID: "infra-env-456",
		imageURL:          "https://images.example.com/discovery.iso?sig=abc",
		getClusterStatus:  "installing",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts_mgmt/v1/access_token", func(w http.ResponseWriter, r *http.Request) {
		f.pullSecretCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer fake-access-token" {
			t.Errorf("expected bearer token on pull secret request, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.pullSecretBody))
	})
	mux.HandleFunc("/api/assisted-install/v2/clusters", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		f.createClusterCalls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastCreateClusterReq = body
		if f.clusterCreateFail != 0 {
			writeJSON(w, f.clusterCreateFail, map[string]string{"reason": "invalid cluster spec"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":                f.createdClusterID,
			"name":              body["name"],
			"openshift_version": body["openshift_version"],
			"status":            "insufficient",
		})
	})
	mux.HandleFunc("/api/assisted-install/v2/clusters/", func(w http.ResponseWriter, r *http.Request) {
		f.getClusterCalls++
		id := strings.TrimPrefix(r.URL.Path, "/api/assisted-install/v2/clusters/")
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                id,
			"name":              "my-cluster",
			"openshift_version": "4.19.10",
			"status":            f.getClusterStatus,
			"status_info":       "installing cluster",
		})
	})
	mux.HandleFunc("/api/assisted-install/v2/infra-envs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		f.createInfraEnvCalls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastCreateInfraEnvReq = body
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":   f.createdInfraEnvID,
			"name": body["name"],
		})
	})
	mux.HandleFunc("/api/assisted-install/v2/infra-envs/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/downloads/image-url"):
			f.imageURLCalls++
			writeJSON(w, http.StatusOK, map[string]string{"url": f.imageURL})
		case strings.HasSuffix(r.URL.Path, "/hosts"):
			f.hostsCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(f.hostsBody))
		default:
			http.NotFound(w, r)
		}
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// authenticatedAssistedSession completes a device login (against the
// given SSO fake) and returns the resulting session ID, ready for
// authenticated cluster-provisioning calls against assisted.
func authenticatedAssistedSession(t *testing.T, h *AssistedOCMHandler, sso *fakeRedHatSSO) string {
	t.Helper()
	return authenticatedTestSession(t, h, sso)
}

func TestHandleCreateCluster_Success(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster","openshiftVersion":"4.19.10","baseDnsDomain":"example.com","cpuArchitecture":"x86_64","sshPublicKey":"ssh-ed25519 AAAA"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp createClusterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ID != "cluster-123" {
		t.Errorf("expected cluster id cluster-123, got %q", resp.ID)
	}

	if assisted.pullSecretCalls != 1 {
		t.Errorf("expected exactly 1 pull secret fetch, got %d", assisted.pullSecretCalls)
	}
	if assisted.createClusterCalls != 1 {
		t.Errorf("expected exactly 1 create cluster call, got %d", assisted.createClusterCalls)
	}

	// The pull secret must be forwarded to the Assisted Service, but
	// never appear in the response the browser sees.
	if got, _ := assisted.lastCreateClusterReq["pull_secret"].(string); got == "" {
		t.Error("expected pull_secret to be forwarded to Assisted Service")
	}
	if strings.Contains(rec.Body.String(), "auths") {
		t.Error("pull secret must never be exposed to the browser")
	}
}

func TestHandleCreateCluster_MissingFields(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(`{}`))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateCluster_NotAuthenticated(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = nil
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(`{"name":"x","openshiftVersion":"4.19.10"}`))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateCluster_ValidationErrorPropagated(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	assisted.clusterCreateFail = http.StatusBadRequest
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(`{"name":"x","openshiftVersion":"4.19.10"}`))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 propagated from Assisted Service, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid cluster spec") {
		t.Errorf("expected upstream error message surfaced, got %s", rec.Body.String())
	}
}

func TestHandleCreateCluster_PullSecretCachedAcrossCalls(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(`{"name":"my-cluster","openshiftVersion":"4.19.10"}`))
		req.SetPathValue("sessionId", sessionID)
		rec := httptest.NewRecorder()
		h.HandleCreateCluster(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	if assisted.pullSecretCalls != 1 {
		t.Errorf("expected pull secret to be fetched exactly once (cached), got %d calls", assisted.pullSecretCalls)
	}
	if assisted.createClusterCalls != 2 {
		t.Errorf("expected 2 create cluster calls, got %d", assisted.createClusterCalls)
	}
}

func TestHandleCreateCluster_TopologyDefaultsToFull(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster","openshiftVersion":"4.19.10"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, ok := assisted.lastCreateClusterReq["control_plane_count"].(float64)
	if !ok {
		t.Fatalf("expected control_plane_count to be set on upstream request, got %#v", assisted.lastCreateClusterReq)
	}
	if got != 3 {
		t.Errorf("expected control_plane_count 3 for default (Full) topology, got %v", got)
	}
}

func TestHandleCreateCluster_TopologySNO(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster","openshiftVersion":"4.19.10","topology":"SNO"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, ok := assisted.lastCreateClusterReq["control_plane_count"].(float64)
	if !ok {
		t.Fatalf("expected control_plane_count to be set on upstream request, got %#v", assisted.lastCreateClusterReq)
	}
	if got != 1 {
		t.Errorf("expected control_plane_count 1 for SNO topology, got %v", got)
	}
}

func TestHandleCreateCluster_TopologyCompact(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster","openshiftVersion":"4.19.10","topology":"Compact"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, ok := assisted.lastCreateClusterReq["control_plane_count"].(float64)
	if !ok {
		t.Fatalf("expected control_plane_count to be set on upstream request, got %#v", assisted.lastCreateClusterReq)
	}
	if got != 3 {
		t.Errorf("expected control_plane_count 3 for Compact topology, got %v", got)
	}
}

func TestHandleCreateCluster_InvalidTopologyRejected(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster","openshiftVersion":"4.19.10","topology":"Bogus"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/clusters/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateCluster(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid topology, got %d: %s", rec.Code, rec.Body.String())
	}
	if assisted.createClusterCalls != 0 {
		t.Errorf("expected no upstream call for invalid topology, got %d", assisted.createClusterCalls)
	}
}

func TestHandleCreateInfraEnv_Success(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	body := `{"name":"my-cluster_infra-env","clusterId":"cluster-123","sshAuthorizedKey":"ssh-ed25519 AAAA","cpuArchitecture":"x86_64"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/infra-envs/"+sessionID, strings.NewReader(body))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateInfraEnv(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp createInfraEnvResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ID != "infra-env-456" {
		t.Errorf("expected infra-env id infra-env-456, got %q", resp.ID)
	}

	if got, _ := assisted.lastCreateInfraEnvReq["image_type"].(string); got != defaultAssistedImageType {
		t.Errorf("expected default image_type %q, got %q", defaultAssistedImageType, got)
	}
	if got, _ := assisted.lastCreateInfraEnvReq["cluster_id"].(string); got != "cluster-123" {
		t.Errorf("expected cluster_id forwarded, got %q", got)
	}
}

func TestHandleCreateInfraEnv_MissingName(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodPost, "/api/ui/assisted/infra-envs/"+sessionID, strings.NewReader(`{}`))
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleCreateInfraEnv(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetImageURL(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/infra-envs/"+sessionID+"/infra-env-456/image-url", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("infraEnvId", "infra-env-456")
	rec := httptest.NewRecorder()
	h.HandleGetImageURL(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp imageURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.URL != assisted.imageURL {
		t.Errorf("expected image URL %q, got %q", assisted.imageURL, resp.URL)
	}
	if assisted.imageURLCalls != 1 {
		t.Errorf("expected 1 image-url call, got %d", assisted.imageURLCalls)
	}
}

func TestHandleGetImageURL_MissingInfraEnvID(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/infra-envs/"+sessionID+"//image-url", nil)
	req.SetPathValue("sessionId", sessionID)
	rec := httptest.NewRecorder()
	h.HandleGetImageURL(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListHosts(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	assisted.hostsBody = `[
		{
			"id": "host-1",
			"requested_hostname": "master-0",
			"status": "known",
			"status_info": "Host is ready",
			"role": "master",
			"inventory": "{\"cpu\":{\"count\":8},\"memory\":{\"physical_bytes\":34359738368},\"disks\":[{},{}],\"interfaces\":[{}]}",
			"validations_info": "{}"
		},
		{
			"id": "host-2",
			"requested_hostname": "",
			"status": "discovering",
			"status_info": "",
			"role": "auto-assign",
			"inventory": "",
			"validations_info": ""
		}
	]`
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/infra-envs/"+sessionID+"/infra-env-456/hosts", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("infraEnvId", "infra-env-456")
	rec := httptest.NewRecorder()
	h.HandleListHosts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp listHostsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d: %+v", len(resp.Hosts), resp.Hosts)
	}
	h1 := resp.Hosts[0]
	if h1.ID != "host-1" || h1.Status != "known" || h1.Role != "master" {
		t.Errorf("unexpected host-1 fields: %+v", h1)
	}
	if h1.CPUCores != 8 {
		t.Errorf("expected 8 CPU cores parsed from inventory, got %d", h1.CPUCores)
	}
	if h1.MemoryBytes != 34359738368 {
		t.Errorf("expected memory bytes parsed from inventory, got %d", h1.MemoryBytes)
	}
	if h1.DiskCount != 2 {
		t.Errorf("expected 2 disks parsed from inventory, got %d", h1.DiskCount)
	}
	if h1.NicCount != 1 {
		t.Errorf("expected 1 NIC parsed from inventory, got %d", h1.NicCount)
	}

	h2 := resp.Hosts[1]
	if h2.Status != "discovering" || h2.CPUCores != 0 {
		t.Errorf("expected host-2 with no inventory to have zero-value hardware fields, got %+v", h2)
	}
}

func TestHandleListHosts_EmptyDiscoveryState(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	assisted.hostsBody = `[]`
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/infra-envs/"+sessionID+"/infra-env-456/hosts", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("infraEnvId", "infra-env-456")
	rec := httptest.NewRecorder()
	h.HandleListHosts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp listHostsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Hosts) != 0 {
		t.Fatalf("expected no hosts, got %d", len(resp.Hosts))
	}
}

func TestHandleGetCluster(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	assisted := newFakeAssistedService(t)
	assisted.getClusterStatus = "installing"
	h := newTestAssistedOCMHandler(sso.server.URL, assisted.server.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/clusters/"+sessionID+"/cluster-123", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("clusterId", "cluster-123")
	rec := httptest.NewRecorder()
	h.HandleGetCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp getClusterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ID != "cluster-123" || resp.Status != "installing" {
		t.Errorf("unexpected cluster response: %+v", resp)
	}
	if assisted.getClusterCalls != 1 {
		t.Errorf("expected 1 get cluster call, got %d", assisted.getClusterCalls)
	}
}

func TestHandleGetCluster_NotAuthenticated(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	sso.pollResults = nil
	h := newTestAssistedOCMHandler(sso.server.URL, "")
	sessionID := startSession(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/clusters/"+sessionID+"/cluster-123", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("clusterId", "cluster-123")
	rec := httptest.NewRecorder()
	h.HandleGetCluster(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetCluster_UpstreamStatusPassedThrough(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	// Point the "assisted service" at a server that returns 404 for
	// the cluster lookup itself (e.g. unknown cluster ID) — this is a
	// legitimate upstream status that should pass through as-is, not
	// get flattened to a generic 502.
	badAssisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/accounts_mgmt/v1/access_token" {
			writeJSON(w, http.StatusOK, map[string]string{"error": "not relevant"})
			return
		}
		http.NotFound(w, r)
	}))
	defer badAssisted.Close()

	h := newTestAssistedOCMHandler(sso.server.URL, badAssisted.URL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/clusters/"+sessionID+"/cluster-123", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("clusterId", "cluster-123")
	rec := httptest.NewRecorder()
	h.HandleGetCluster(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected upstream 404 passed through, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetCluster_UnreachableServiceMapsTo502(t *testing.T) {
	sso := newFakeRedHatSSO(t)
	// A server that accepts the pull-secret-irrelevant path but is
	// immediately closed for the cluster call, simulating a network
	// failure (connection refused) rather than a valid HTTP response.
	badAssisted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"error": "not relevant"})
	}))
	badAssistedURL := badAssisted.URL
	badAssisted.Close() // closed before use: every request now fails to connect

	h := newTestAssistedOCMHandler(sso.server.URL, badAssistedURL)
	sessionID := authenticatedAssistedSession(t, h, sso)

	req := httptest.NewRequest(http.MethodGet, "/api/ui/assisted/clusters/"+sessionID+"/cluster-123", nil)
	req.SetPathValue("sessionId", sessionID)
	req.SetPathValue("clusterId", "cluster-123")
	rec := httptest.NewRecorder()
	h.HandleGetCluster(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for unreachable upstream, got %d: %s", rec.Code, rec.Body.String())
	}
}
