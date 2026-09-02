import type { ClusterDetailTabProps } from "@fleetshift/common";
import { ResourceApiError } from "@fleetshift/common";
import {
  Alert,
  Button,
  EmptyState,
  EmptyStateBody,
  Label,
  Stack,
  StackItem,
  Toolbar,
  ToolbarContent,
  ToolbarItem,
} from "@patternfly/react-core";
import {
  SkeletonTableBody,
  SkeletonTableHead,
} from "@patternfly/react-component-groups";
import {
  DataView,
  DataViewState,
} from "@patternfly/react-data-view/dist/dynamic/DataView";
import {
  DataViewTable,
  type DataViewTh,
  type DataViewTr,
} from "@patternfly/react-data-view/dist/dynamic/DataViewTable";
import { DownloadIcon } from "@patternfly/react-icons";
import { Tbody, Td, Tr } from "@patternfly/react-table";
import { useCallback, useEffect, useMemo, useState } from "react";

import type { ClusterTopology, HostOption, OcmAccount } from "./api";
import {
  getFleetShiftCluster,
  getImageUrl,
  listHosts,
  minimumHostsForTopology,
  topologyLabel,
} from "./api";
import {
  clearStoredOcmSessionId,
  getStoredOcmSessionId,
  setStoredOcmSessionId,
} from "./ocmSessionStorage";
import RedHatAuthStep from "./RedHatAuthStep";

const POLL_INTERVAL_MS = 8000;

const columns: DataViewTh[] = [
  "Hostname",
  "Status",
  "Role",
  "CPU",
  "Memory",
  "Disks",
  "NICs",
];

function statusColor(
  status: string,
): "green" | "blue" | "orange" | "red" | "grey" {
  switch (status) {
    case "known":
    case "ready":
    case "installed":
    case "installing-in-progress":
      return "green";
    case "discovering":
    case "pending-for-input":
    case "preparing-for-installation":
      return "blue";
    case "insufficient":
    case "disconnected":
    case "disabled":
      return "orange";
    case "error":
      return "red";
    default:
      return "grey";
  }
}

function formatBytes(bytes: number): string {
  if (!bytes) return "—";
  const gb = bytes / 1024 ** 3;
  return `${gb.toFixed(1)} GiB`;
}

export default function HostDiscoveryTab({
  clusterId,
}: ClusterDetailTabProps) {
  const [sessionId, setSessionId] = useState<string | null>(() =>
    getStoredOcmSessionId(),
  );
  const [infraEnvId, setInfraEnvId] = useState<string | null>(null);
  const [topology, setTopology] = useState<ClusterTopology | null>(null);
  const [hosts, setHosts] = useState<HostOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isoUrl, setIsoUrl] = useState<string | null>(null);
  const [isoLoading, setIsoLoading] = useState(false);

  // Resolve the Assisted InfraEnv ID from FleetShift's own managed
  // resource record for this cluster (recorded at creation time — see
  // CreateAssistedWizard.tsx's registerFleetShiftCluster call).
  useEffect(() => {
    if (!clusterId) return;
    let cancelled = false;
    getFleetShiftCluster(clusterId)
      .then((cluster) => {
        if (!cancelled) {
          setInfraEnvId(cluster.spec.infraEnvId || null);
          setTopology(cluster.spec.topology || null);
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError(
            err instanceof Error ? err.message : "Failed to load cluster",
          );
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [clusterId]);

  const fetchHosts = useCallback(
    async (silent = false) => {
      if (!sessionId || !infraEnvId) return;
      if (!silent) setLoading(true);
      try {
        const res = await listHosts(sessionId, infraEnvId);
        setHosts(res.hosts);
        setError(null);
      } catch (err) {
        if (err instanceof ResourceApiError && err.status === 401) {
          // OCM session expired server-side (30 min TTL) — drop it so
          // the sign-in prompt reappears.
          clearStoredOcmSessionId();
          setSessionId(null);
        } else {
          setError(err instanceof Error ? err.message : "Failed to load hosts");
        }
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [sessionId, infraEnvId],
  );

  useEffect(() => {
    fetchHosts();
  }, [fetchHosts]);

  useEffect(() => {
    if (!sessionId || !infraEnvId) return;
    const id = setInterval(() => fetchHosts(true), POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, [sessionId, infraEnvId, fetchHosts]);

  const handleAuthenticated = useCallback(
    (newSessionId: string, _account: OcmAccount | null) => {
      setStoredOcmSessionId(newSessionId);
      setSessionId(newSessionId);
    },
    [],
  );

  const handleDownloadIso = useCallback(async () => {
    if (!sessionId || !infraEnvId) return;
    setIsoLoading(true);
    try {
      const res = await getImageUrl(sessionId, infraEnvId);
      setIsoUrl(res.url);
      window.open(res.url, "_blank", "noreferrer");
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to fetch ISO download URL",
      );
    } finally {
      setIsoLoading(false);
    }
  }, [sessionId, infraEnvId]);

  const rows: DataViewTr[] = useMemo(
    () =>
      hosts.map((host) => [
        host.requestedHostname || host.id,
        {
          cell: (
            <Label color={statusColor(host.status)} isCompact>
              {host.status}
            </Label>
          ),
        },
        host.role || "auto-assign",
        host.cpuCores ? `${host.cpuCores} cores` : "—",
        formatBytes(host.memoryBytes),
        host.diskCount || "—",
        host.nicCount || "—",
      ]),
    [hosts],
  );

  if (!sessionId) {
    return (
      <Stack hasGutter>
        <StackItem>
          <Alert variant="info" isInline title="Red Hat sign-in required">
            Sign in again to resume polling the Assisted Installer Service for
            this cluster&apos;s discovered hosts.
          </Alert>
        </StackItem>
        <StackItem>
          <RedHatAuthStep onAuthenticated={handleAuthenticated} />
        </StackItem>
      </Stack>
    );
  }

  const activeState = loading
    ? DataViewState.loading
    : error
      ? "error"
      : hosts.length === 0
        ? "empty"
        : undefined;

  const requiredHosts = topology ? minimumHostsForTopology(topology) : null;
  const hasEnoughHosts = requiredHosts !== null && hosts.length >= requiredHosts;

  return (
    <Stack hasGutter>
      <StackItem>
        <Toolbar>
          <ToolbarContent>
            {topology && requiredHosts !== null && (
              <ToolbarItem>
                <Label color={hasEnoughHosts ? "green" : "blue"} isCompact>
                  {topologyLabel(topology)} — {hosts.length} of{" "}
                  {requiredHosts} required host
                  {requiredHosts === 1 ? "" : "s"} discovered
                </Label>
              </ToolbarItem>
            )}
            <ToolbarItem align={{ default: "alignEnd" }}>
              <Button
                variant="secondary"
                icon={<DownloadIcon />}
                onClick={handleDownloadIso}
                isDisabled={!infraEnvId || isoLoading}
              >
                {isoLoading ? "Fetching ISO URL..." : "Download Discovery ISO"}
              </Button>
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
      </StackItem>
      {isoUrl && (
        <StackItem>
          <Alert variant="info" isInline title="Discovery ISO download started">
            If the download didn&apos;t open automatically,{" "}
            <a href={isoUrl} target="_blank" rel="noreferrer">
              click here
            </a>
            . Presigned URLs expire after about an hour.
          </Alert>
        </StackItem>
      )}
      <StackItem>
        <DataView activeState={activeState}>
          <DataViewTable
            aria-label="Discovered hosts table"
            columns={columns}
            rows={rows}
            headStates={{ loading: <SkeletonTableHead columns={columns} /> }}
            bodyStates={{
              loading: (
                <SkeletonTableBody
                  rowsCount={3}
                  columnsCount={columns.length}
                />
              ),
              empty: (
                <Tbody>
                  <Tr>
                    <Td colSpan={columns.length}>
                      <EmptyState headingLevel="h3" titleText="No hosts yet">
                        <EmptyStateBody>
                          Boot your servers with the Discovery ISO above —
                          hosts will appear here automatically once they check
                          in with the Assisted Installer Service.
                        </EmptyStateBody>
                      </EmptyState>
                    </Td>
                  </Tr>
                </Tbody>
              ),
              error: (
                <Tbody>
                  <Tr>
                    <Td colSpan={columns.length}>
                      <EmptyState
                        headingLevel="h3"
                        titleText="Unable to load hosts"
                      >
                        <EmptyStateBody>{error}</EmptyStateBody>
                      </EmptyState>
                    </Td>
                  </Tr>
                </Tbody>
              ),
            }}
          />
        </DataView>
      </StackItem>
    </Stack>
  );
}
