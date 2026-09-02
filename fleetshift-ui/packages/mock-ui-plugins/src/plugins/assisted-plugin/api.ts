import { createApiClient } from "@fleetshift/common";

// Client for the Go backend's Red Hat OCM (OpenShift Cluster Manager)
// integration. The browser never talks to Red Hat directly — sso.redhat.com
// and api.openshift.com are proxied through /api/ui/assisted/* because
// they aren't known to support CORS for browser-originated requests (same
// reasoning as the GitHub signing-key proxy in the signing-plugin). See
// fleetshift-server/internal/transport/http/assisted_ocm.go.
const client = createApiClient("/api/ui/assisted");

// Client for FleetShift's own managed-resource API for the assisted
// addon (see fleetshift-server/internal/addon/assisted). This is a
// same-origin, unauthenticated-by-CORS-concerns call (unlike the OCM
// client above) — it talks directly to the FleetShift server, not to
// Red Hat.
const fleetshiftClient = createApiClient("/apis/assisted.fleetshift.io/v1");

export interface StartDeviceAuthResponse {
  sessionId: string;
  verificationUri: string;
  verificationUriComplete: string;
  userCode: string;
  expiresIn: number;
  interval: number;
}

export type DeviceAuthStatus =
  | "pending"
  | "complete"
  | "expired"
  | "error"
  | "not_found";

export interface PollDeviceAuthResponse {
  status: DeviceAuthStatus;
  interval?: number;
  error?: string;
}

export interface OcmAccount {
  username: string;
  email: string;
}

export interface OpenShiftVersionOption {
  version: string;
  displayName: string;
  cpuArchitectures: string[];
  supportLevel: string;
  default: boolean;
}

export interface OpenShiftVersionsResponse {
  versions: OpenShiftVersionOption[];
}

/** Begin a Red Hat SSO device-authorization-grant login. */
export function startDeviceAuth(): Promise<StartDeviceAuthResponse> {
  return client.post<StartDeviceAuthResponse>("/auth/device");
}

/**
 * Make a single, non-blocking poll attempt for a device login. Callers
 * should re-invoke this on the interval returned by startDeviceAuth (or
 * PollDeviceAuthResponse.interval, if present) until status is anything
 * other than "pending".
 */
export function pollDeviceAuth(
  sessionId: string,
): Promise<PollDeviceAuthResponse> {
  return client.get<PollDeviceAuthResponse>(
    `/auth/device/poll/${encodeURIComponent(sessionId)}`,
  );
}

/** Fetch the signed-in Red Hat account for display confirmation. */
export function getOcmAccount(sessionId: string): Promise<OcmAccount> {
  return client.get<OcmAccount>(`/account/${encodeURIComponent(sessionId)}`);
}

/** Fetch the real list of OpenShift versions supported by Assisted Service. */
export function getOpenShiftVersions(
  sessionId: string,
): Promise<OpenShiftVersionsResponse> {
  return client.get<OpenShiftVersionsResponse>(
    `/openshift-versions/${encodeURIComponent(sessionId)}`,
  );
}

/**
 * Cluster topology choice, mirroring the Assisted Service's
 * control_plane_count field: "SNO" (single-node OpenShift, 1 host),
 * "Compact" (3 hosts, each control plane + worker), or "Full" (3+
 * dedicated control-plane hosts plus separate worker hosts). See
 * fleetshift-server/internal/transport/http/assisted_ocm_clusters.go
 * for how this maps to control_plane_count server-side.
 */
export type ClusterTopology = "SNO" | "Compact" | "Full";

export interface TopologyOption {
  value: ClusterTopology;
  label: string;
  description: string;
}

/** Display metadata for each topology choice, used by the wizard's
 * topology selector, the Review step, and the Hosts tab's progress
 * indicator. */
export const TOPOLOGY_OPTIONS: TopologyOption[] = [
  {
    value: "SNO",
    label: "Single Node OpenShift (SNO)",
    description: "1 host acts as both control plane and worker.",
  },
  {
    value: "Compact",
    label: "Compact (3 hosts)",
    description: "3 hosts, each acting as both control plane and worker.",
  },
  {
    value: "Full",
    label: "Full / multi-node",
    description:
      "3+ dedicated control-plane hosts, plus separate worker hosts.",
  },
];

/** Human-readable label for a topology value, falling back to the raw
 * value for records created before this field existed. */
export function topologyLabel(topology: string): string {
  return (
    TOPOLOGY_OPTIONS.find((o) => o.value === topology)?.label ?? topology
  );
}

/** Minimum host count the Assisted Service requires before it will
 * validate a cluster of the given topology as installable. */
export function minimumHostsForTopology(topology: ClusterTopology): number {
  return topology === "SNO" ? 1 : 3;
}

export interface CreateClusterRequest {
  name: string;
  openshiftVersion: string;
  baseDnsDomain?: string;
  cpuArchitecture?: string;
  sshPublicKey?: string;
  topology?: ClusterTopology;
}

export interface CreateClusterResponse {
  id: string;
  name: string;
  openshiftVersion: string;
  status: string;
}

/**
 * Create a real cluster definition on the Assisted Service
 * (POST /v2/clusters). The pull secret is fetched and merged in
 * server-side — never sent from or returned to the browser.
 */
export function createCluster(
  sessionId: string,
  req: CreateClusterRequest,
): Promise<CreateClusterResponse> {
  return client.post<CreateClusterResponse>(
    `/clusters/${encodeURIComponent(sessionId)}`,
    req,
  );
}

export interface CreateInfraEnvRequest {
  name: string;
  clusterId?: string;
  sshAuthorizedKey?: string;
  cpuArchitecture?: string;
  imageType?: string;
}

export interface CreateInfraEnvResponse {
  id: string;
  name: string;
}

/**
 * Create the InfraEnv that actually generates the Discovery ISO
 * (POST /v2/infra-envs). Call after {@link createCluster}, passing the
 * resulting cluster ID.
 */
export function createInfraEnv(
  sessionId: string,
  req: CreateInfraEnvRequest,
): Promise<CreateInfraEnvResponse> {
  return client.post<CreateInfraEnvResponse>(
    `/infra-envs/${encodeURIComponent(sessionId)}`,
    req,
  );
}

export interface ImageUrlResponse {
  url: string;
}

/** Fetch the presigned Discovery ISO download URL for an InfraEnv. */
export function getImageUrl(
  sessionId: string,
  infraEnvId: string,
): Promise<ImageUrlResponse> {
  return client.get<ImageUrlResponse>(
    `/infra-envs/${encodeURIComponent(sessionId)}/${encodeURIComponent(infraEnvId)}/image-url`,
  );
}

export interface HostOption {
  id: string;
  requestedHostname: string;
  status: string;
  statusInfo: string;
  role: string;
  cpuCores: number;
  memoryBytes: number;
  diskCount: number;
  nicCount: number;
  validationsInfo?: string;
}

export interface ListHostsResponse {
  hosts: HostOption[];
}

/**
 * Poll the Assisted Service for hosts discovered so far in an
 * InfraEnv. Hosts self-register with the Assisted Service directly
 * once they boot the Discovery ISO — FleetShift never talks to them.
 */
export function listHosts(
  sessionId: string,
  infraEnvId: string,
): Promise<ListHostsResponse> {
  return client.get<ListHostsResponse>(
    `/infra-envs/${encodeURIComponent(sessionId)}/${encodeURIComponent(infraEnvId)}/hosts`,
  );
}

export interface GetClusterResponse {
  id: string;
  name: string;
  openshiftVersion: string;
  status: string;
  statusInfo: string;
}

/** Poll the Assisted Service for the cluster's current install status. */
export function getCluster(
  sessionId: string,
  clusterId: string,
): Promise<GetClusterResponse> {
  return client.get<GetClusterResponse>(
    `/clusters/${encodeURIComponent(sessionId)}/${encodeURIComponent(clusterId)}`,
  );
}

export interface AssistedClusterSpec {
  releaseVersion: string;
  baseDomain: string;
  cpuArchitecture: string;
  assistedClusterId: string;
  infraEnvId: string;
  fips: boolean;
  disconnected: boolean;
  topology?: ClusterTopology;
}

export interface AssistedClusterResource {
  name: string;
  uid: string;
  spec: AssistedClusterSpec;
  state: string;
}

/**
 * Register the Assisted Service cluster as a FleetShift managed
 * resource (see fleetshift-server/internal/addon/assisted), so it
 * appears in FleetShift's own Clusters list immediately, following the
 * same pattern as the kind and gcphcp cluster providers.
 */
export function registerFleetShiftCluster(
  clusterName: string,
  spec: AssistedClusterSpec,
): Promise<AssistedClusterResource> {
  return fleetshiftClient.post<AssistedClusterResource>(
    `/clusters?cluster_id=${encodeURIComponent(clusterName)}`,
    { spec },
  );
}

/**
 * Fetch the FleetShift-side managed resource for an Assisted cluster
 * (not the Assisted Service's own cluster object) — used by the Hosts
 * tab to recover the Assisted Service cluster/InfraEnv IDs recorded at
 * creation time.
 */
export function getFleetShiftCluster(
  clusterId: string,
): Promise<AssistedClusterResource> {
  return fleetshiftClient.get<AssistedClusterResource>(
    `/clusters/${encodeURIComponent(clusterId)}`,
  );
}
