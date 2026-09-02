package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// This file extends AssistedOCMHandler (see assisted_ocm.go) with the
// Assisted Service v2 cluster-provisioning proxy: cluster creation,
// InfraEnv creation (which is what actually generates the Discovery
// ISO), ISO download URL retrieval, host discovery polling, and cluster
// status polling. All calls use the OCM session's access token (with
// automatic refresh via ensureFreshAccessToken) and never expose the
// pull secret to the browser — it is fetched server-side once per
// session and cached on the ocmSession.
//
// See OME-263 / OME-273 for the tracking work and
// poc/ocp-archive/e2e/auth.go:FetchPullSecret for the pull secret
// fetch pattern this is adapted from.

// ensurePullSecret returns the session's cached Red Hat pull secret,
// fetching and caching it on first use via
// POST /api/accounts_mgmt/v1/access_token. Must be called with a valid,
// already-authenticated session (see authenticatedSession).
func (h *AssistedOCMHandler) ensurePullSecret(ctx context.Context, sess *ocmSession) (string, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.pullSecret != "" {
		return sess.pullSecret, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.AssistedServiceBaseURL+"/api/accounts_mgmt/v1/access_token", strings.NewReader("{}"))
	if err != nil {
		return "", fmt.Errorf("building pull secret request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sess.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching pull secret: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading pull secret response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pull secret API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("pull secret is not valid JSON: %w", err)
	}
	if _, ok := parsed["auths"]; !ok {
		return "", fmt.Errorf("pull secret response missing 'auths' field")
	}

	sess.pullSecret = string(body)
	return sess.pullSecret, nil
}

// postJSON performs an authenticated POST against the Assisted Service
// API, encoding body as JSON and decoding the JSON response into out.
// Non-2xx responses are surfaced as an *assistedAPIError so callers can
// map them to meaningful HTTP status codes for the UI.
func (h *AssistedOCMHandler) postJSON(ctx context.Context, accessToken, url string, body any, out any) error {
	return h.doJSON(ctx, http.MethodPost, accessToken, url, body, out)
}

func (h *AssistedOCMHandler) doJSON(ctx context.Context, method, accessToken, url string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = strings.NewReader(string(encoded))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("performing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &assistedAPIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// assistedAPIError wraps a non-2xx response from the Assisted Service
// so HTTP handlers can map it to a meaningful status code and message
// for the UI instead of a generic 502.
type assistedAPIError struct {
	StatusCode int
	Body       string
}

func (e *assistedAPIError) Error() string {
	return fmt.Sprintf("assisted service returned status %d: %s", e.StatusCode, e.Body)
}

// writeAssistedError maps an error from a proxied Assisted Service call
// to an HTTP response. *assistedAPIError statuses are passed through
// (e.g. 400 validation errors), everything else (network failures,
// decoding errors) is reported as 502 Bad Gateway.
func writeAssistedError(w http.ResponseWriter, err error) {
	var apiErr *assistedAPIError
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status < 400 || status >= 600 {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, map[string]string{"error": apiErr.Body})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

// --- create cluster ---

// Cluster topology choices exposed by the create-cluster wizard. These
// map to the Assisted Service's control_plane_count field (see
// controlPlaneCountForTopology): "Compact" and "Full" both use 3
// control-plane nodes -- the Assisted Service itself doesn't record
// intended worker count until hosts are actually assigned roles, so
// the distinction is FleetShift's own recorded intent, persisted on
// the managed resource for display in the Hosts/Overview tabs.
const (
	assistedTopologySNO     = "SNO"
	assistedTopologyCompact = "Compact"
	assistedTopologyFull    = "Full"
)

func isValidAssistedTopology(topology string) bool {
	switch topology {
	case assistedTopologySNO, assistedTopologyCompact, assistedTopologyFull:
		return true
	default:
		return false
	}
}

// controlPlaneCountForTopology maps a FleetShift topology choice to
// the Assisted Service's control_plane_count field: 1 for SNO
// (single-node OpenShift), 3 for Compact or Full.
func controlPlaneCountForTopology(topology string) int {
	if topology == assistedTopologySNO {
		return 1
	}
	return 3
}

// createClusterRequest is the browser-facing request body for creating
// an Assisted Service cluster. The pull secret is never accepted from
// or returned to the browser — it's fetched server-side via
// ensurePullSecret.
type createClusterRequest struct {
	Name             string `json:"name"`
	OpenShiftVersion string `json:"openshiftVersion"`
	BaseDNSDomain    string `json:"baseDnsDomain"`
	CPUArchitecture  string `json:"cpuArchitecture"`
	SSHPublicKey     string `json:"sshPublicKey,omitempty"`
	// Topology is one of "SNO", "Compact", or "Full" — see the
	// assistedTopology* constants. Defaults to "Full" when omitted.
	Topology string `json:"topology,omitempty"`
}

// assistedClusterCreateParams mirrors the subset of the Assisted
// Service v2 "cluster-create-params" object this proxy populates.
// https://api.openshift.com/api/assisted-install/v2/clusters
type assistedClusterCreateParams struct {
	Name              string `json:"name"`
	OpenshiftVersion  string `json:"openshift_version"`
	PullSecret        string `json:"pull_secret"`
	BaseDNSDomain     string `json:"base_dns_domain,omitempty"`
	CPUArchitecture   string `json:"cpu_architecture,omitempty"`
	SSHPublicKey      string `json:"ssh_public_key,omitempty"`
	ControlPlaneCount *int   `json:"control_plane_count,omitempty"`
}

type clusterResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	OpenshiftVersion string `json:"openshift_version"`
	Status           string `json:"status"`
	StatusInfo       string `json:"status_info"`
}

type createClusterResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	OpenShiftVersion string `json:"openshiftVersion"`
	Status           string `json:"status"`
}

// HandleCreateCluster proxies POST /v2/clusters, fetching the pull
// secret server-side and merging it into the request body.
// Route: POST /api/ui/assisted/clusters/{sessionId}
func (h *AssistedOCMHandler) HandleCreateCluster(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}

	var req createClusterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.OpenShiftVersion) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and openshiftVersion are required"})
		return
	}

	topology := strings.TrimSpace(req.Topology)
	if topology == "" {
		topology = assistedTopologyFull
	}
	if !isValidAssistedTopology(topology) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topology must be one of SNO, Compact, Full"})
		return
	}

	pullSecret, err := h.ensurePullSecret(r.Context(), sess)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch pull secret", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch Red Hat pull secret"})
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	controlPlaneCount := controlPlaneCountForTopology(topology)
	params := assistedClusterCreateParams{
		Name:              req.Name,
		OpenshiftVersion:  req.OpenShiftVersion,
		PullSecret:        pullSecret,
		BaseDNSDomain:     req.BaseDNSDomain,
		CPUArchitecture:   req.CPUArchitecture,
		SSHPublicKey:      req.SSHPublicKey,
		ControlPlaneCount: &controlPlaneCount,
	}

	var created clusterResponse
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/clusters"
	if err := h.postJSON(r.Context(), accessToken, url, params, &created); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to create assisted cluster", "error", err)
		writeAssistedError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, createClusterResponse{
		ID:               created.ID,
		Name:             created.Name,
		OpenShiftVersion: created.OpenshiftVersion,
		Status:           created.Status,
	})
}

// --- create infra-env ---

// createInfraEnvRequest is the browser-facing request body for
// creating an Assisted Service InfraEnv — the object that actually
// generates the Discovery ISO once created.
type createInfraEnvRequest struct {
	Name             string `json:"name"`
	ClusterID        string `json:"clusterId"`
	SSHAuthorizedKey string `json:"sshAuthorizedKey,omitempty"`
	CPUArchitecture  string `json:"cpuArchitecture,omitempty"`
	ImageType        string `json:"imageType,omitempty"`
}

// assistedInfraEnvCreateParams mirrors the subset of the Assisted
// Service v2 "infra-env-create-params" object this proxy populates.
type assistedInfraEnvCreateParams struct {
	Name             string `json:"name"`
	PullSecret       string `json:"pull_secret"`
	ClusterID        string `json:"cluster_id,omitempty"`
	SSHAuthorizedKey string `json:"ssh_authorized_key,omitempty"`
	CPUArchitecture  string `json:"cpu_architecture,omitempty"`
	ImageType        string `json:"image_type,omitempty"`
}

type infraEnvResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type createInfraEnvResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const defaultAssistedImageType = "full-iso"

// HandleCreateInfraEnv proxies POST /v2/infra-envs, which is what
// actually generates the Discovery ISO server-side (retrieved
// separately via HandleGetImageURL).
// Route: POST /api/ui/assisted/infra-envs/{sessionId}
func (h *AssistedOCMHandler) HandleCreateInfraEnv(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}

	var req createInfraEnvRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	pullSecret, err := h.ensurePullSecret(r.Context(), sess)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch pull secret", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to fetch Red Hat pull secret"})
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	imageType := req.ImageType
	if imageType == "" {
		imageType = defaultAssistedImageType
	}

	params := assistedInfraEnvCreateParams{
		Name:             req.Name,
		PullSecret:       pullSecret,
		ClusterID:        req.ClusterID,
		SSHAuthorizedKey: req.SSHAuthorizedKey,
		CPUArchitecture:  req.CPUArchitecture,
		ImageType:        imageType,
	}

	var created infraEnvResponse
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/infra-envs"
	if err := h.postJSON(r.Context(), accessToken, url, params, &created); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to create assisted infra-env", "error", err)
		writeAssistedError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, createInfraEnvResponse{ID: created.ID, Name: created.Name})
}

// --- ISO download URL ---

type imageURLResponse struct {
	URL string `json:"url"`
}

// HandleGetImageURL proxies GET /v2/infra-envs/{id}/downloads/image-url,
// returning the presigned Discovery ISO download URL.
// Route: GET /api/ui/assisted/infra-envs/{sessionId}/{infraEnvId}/image-url
func (h *AssistedOCMHandler) HandleGetImageURL(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}
	infraEnvID := r.PathValue("infraEnvId")
	if infraEnvID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "infraEnvId is required"})
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	var resp imageURLResponse
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/infra-envs/" + escapePath(infraEnvID) + "/downloads/image-url"
	if err := h.doJSON(r.Context(), http.MethodGet, accessToken, url, nil, &resp); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch ISO image URL", "error", err)
		writeAssistedError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- host discovery ---

// assistedHost mirrors the subset of the Assisted Service v2 "host"
// object this proxy surfaces to the UI. The inventory field on the
// real API is a JSON-encoded string; hostInventorySummary is parsed
// out of it server-side so the browser doesn't need to.
type assistedHost struct {
	ID                string `json:"id"`
	RequestedHostname string `json:"requested_hostname"`
	Status            string `json:"status"`
	StatusInfo        string `json:"status_info"`
	Role              string `json:"role"`
	Inventory         string `json:"inventory"`
	ValidationsInfo   string `json:"validations_info"`
}

type hostInventoryJSON struct {
	CPU struct {
		Count int64 `json:"count"`
	} `json:"cpu"`
	Memory struct {
		PhysicalBytes int64 `json:"physical_bytes"`
	} `json:"memory"`
	Disks      []json.RawMessage `json:"disks"`
	Interfaces []json.RawMessage `json:"interfaces"`
}

// hostOption is the UI-facing shape for a discovered host: parsed
// hardware inventory summary plus status/role/validation info.
type hostOption struct {
	ID                string `json:"id"`
	RequestedHostname string `json:"requestedHostname"`
	Status            string `json:"status"`
	StatusInfo        string `json:"statusInfo"`
	Role              string `json:"role"`
	CPUCores          int64  `json:"cpuCores"`
	MemoryBytes       int64  `json:"memoryBytes"`
	DiskCount         int    `json:"diskCount"`
	NicCount          int    `json:"nicCount"`
	ValidationsInfo   string `json:"validationsInfo,omitempty"`
}

type listHostsResponse struct {
	Hosts []hostOption `json:"hosts"`
}

// HandleListHosts proxies GET /v2/infra-envs/{id}/hosts. This is the
// primary post-wizard polling endpoint: bare-metal hosts booted from
// the Discovery ISO register themselves with the Assisted Service
// directly (not with FleetShift), so this handler's only job is to
// relay the current discovery snapshot for the Hosts tab to render.
// Route: GET /api/ui/assisted/infra-envs/{sessionId}/{infraEnvId}/hosts
func (h *AssistedOCMHandler) HandleListHosts(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}
	infraEnvID := r.PathValue("infraEnvId")
	if infraEnvID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "infraEnvId is required"})
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	var hosts []assistedHost
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/infra-envs/" + escapePath(infraEnvID) + "/hosts"
	if err := h.doJSON(r.Context(), http.MethodGet, accessToken, url, nil, &hosts); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to list assisted hosts", "error", err)
		writeAssistedError(w, err)
		return
	}

	out := make([]hostOption, 0, len(hosts))
	for _, host := range hosts {
		opt := hostOption{
			ID:                host.ID,
			RequestedHostname: host.RequestedHostname,
			Status:            host.Status,
			StatusInfo:        host.StatusInfo,
			Role:              host.Role,
			ValidationsInfo:   host.ValidationsInfo,
		}
		if host.Inventory != "" {
			var inv hostInventoryJSON
			if err := json.Unmarshal([]byte(host.Inventory), &inv); err == nil {
				opt.CPUCores = inv.CPU.Count
				opt.MemoryBytes = inv.Memory.PhysicalBytes
				opt.DiskCount = len(inv.Disks)
				opt.NicCount = len(inv.Interfaces)
			}
		}
		out = append(out, opt)
	}

	writeJSON(w, http.StatusOK, listHostsResponse{Hosts: out})
}

// --- cluster status polling ---

type getClusterResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	OpenShiftVersion string `json:"openshiftVersion"`
	Status           string `json:"status"`
	StatusInfo       string `json:"statusInfo"`
}

// HandleGetCluster proxies GET /v2/clusters/{id} for cluster status
// polling. Route: GET /api/ui/assisted/clusters/{sessionId}/{clusterId}
func (h *AssistedOCMHandler) HandleGetCluster(w http.ResponseWriter, r *http.Request) {
	sess := h.authenticatedSession(w, r, r.PathValue("sessionId"))
	if sess == nil {
		return
	}
	clusterID := r.PathValue("clusterId")
	if clusterID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "clusterId is required"})
		return
	}

	sess.mu.Lock()
	accessToken := sess.accessToken
	sess.mu.Unlock()

	var cluster clusterResponse
	url := h.AssistedServiceBaseURL + "/api/assisted-install/v2/clusters/" + escapePath(clusterID)
	if err := h.doJSON(r.Context(), http.MethodGet, accessToken, url, nil, &cluster); err != nil {
		h.Logger.ErrorContext(r.Context(), "failed to fetch assisted cluster", "error", err)
		writeAssistedError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, getClusterResponse{
		ID:               cluster.ID,
		Name:             cluster.Name,
		OpenShiftVersion: cluster.OpenshiftVersion,
		Status:           cluster.Status,
		StatusInfo:       cluster.StatusInfo,
	})
}

// escapePath percent-encodes a path segment (Assisted Service resource
// IDs are UUIDs in practice, but this guards against surprises).
func escapePath(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "%", "%25"), "/", "%2F")
}
