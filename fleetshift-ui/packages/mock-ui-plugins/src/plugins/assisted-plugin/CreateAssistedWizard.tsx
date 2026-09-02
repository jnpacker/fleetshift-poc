import type { ClusterProviderWizardProps } from "@fleetshift/common";
import { usePluginNavigate } from "@fleetshift/common";
import { Alert, Wizard, WizardStep } from "@patternfly/react-core";
import { useCallback, useState } from "react";

import type {
  ClusterTopology,
  OcmAccount,
  OpenShiftVersionOption,
} from "./api";
import {
  createCluster,
  createInfraEnv,
  getImageUrl,
  getOpenShiftVersions,
  registerFleetShiftCluster,
} from "./api";
import ClusterDetailsStep from "./ClusterDetailsStep";
import GenerateIsoStep from "./GenerateIsoStep";
import InstallationOptionsStep from "./InstallationOptionsStep";
import { setStoredOcmSessionId } from "./ocmSessionStorage";
import RedHatAuthStep from "./RedHatAuthStep";
import ReviewStep from "./ReviewStep";
import WelcomeStep from "./WelcomeStep";

// NOTE: Red Hat SSO sign-in (device-authorization-grant, RFC 8628), the
// OpenShift-version lookup, and cluster/InfraEnv/ISO creation are all
// real, calling the actual Assisted Service / OCM APIs via the Go
// backend proxy — see OME-231, OME-263, OME-264, and
// fleetshift-server/internal/transport/http/assisted_ocm.go +
// assisted_ocm_clusters.go. The pull secret is fetched and merged in
// entirely server-side; the browser never sees it.

export interface AssistedComplianceOptions {
  fips: boolean;
  disconnected: boolean;
}

export interface AssistedFormData {
  clusterName: string;
  ocpVersion: string;
  baseDomain: string;
  cpuArchitecture: string;
  sshPublicKey: string;
  topology: ClusterTopology;
  compliance: AssistedComplianceOptions;
}

const initialFormData: AssistedFormData = {
  clusterName: "",
  ocpVersion: "",
  baseDomain: "",
  cpuArchitecture: "x86_64",
  sshPublicKey: "",
  topology: "Full",
  compliance: {
    fips: false,
    disconnected: false,
  },
};

const CLUSTER_NAME_PATTERN = /^[a-z][-a-z0-9]*$/;

export default function CreateAssistedWizard({
  onClose,
  onSetupNext,
}: ClusterProviderWizardProps) {
  const clusters = usePluginNavigate("core-plugin", "ClustersModule");
  const [formData, setFormData] = useState<AssistedFormData>(initialFormData);
  const [generating, setGenerating] = useState(false);
  const [isoUrl, setIsoUrl] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [ocmSessionId, setOcmSessionId] = useState<string | null>(null);
  const [ocmAccount, setOcmAccount] = useState<OcmAccount | null>(null);
  const [ocpVersions, setOcpVersions] = useState<OpenShiftVersionOption[]>([]);
  const [ocpVersionsLoading, setOcpVersionsLoading] = useState(false);
  const [ocpVersionsError, setOcpVersionsError] = useState<string | null>(null);
  const [assistedClusterId, setAssistedClusterId] = useState<string | null>(
    null,
  );
  const [infraEnvId, setInfraEnvId] = useState<string | null>(null);

  const updateField = useCallback(
    <K extends keyof AssistedFormData>(
      field: K,
      value: AssistedFormData[K],
    ) => {
      setFormData((prev) => ({ ...prev, [field]: value }));
    },
    [],
  );

  const handleRedHatAuthenticated = useCallback(
    (sessionId: string, account: OcmAccount | null) => {
      setOcmSessionId(sessionId);
      setOcmAccount(account);
      // Persist so the cluster detail page's Hosts tab can resume
      // polling the Assisted Service without re-running sign-in.
      setStoredOcmSessionId(sessionId);
      setOcpVersionsLoading(true);
      setOcpVersionsError(null);
      getOpenShiftVersions(sessionId)
        .then((res) => {
          setOcpVersions(res.versions);
          const preferred =
            res.versions.find((v) => v.default) ?? res.versions[0];
          if (preferred) {
            setFormData((prev) =>
              prev.ocpVersion
                ? prev
                : {
                    ...prev,
                    ocpVersion: preferred.version,
                    // Prefer x86_64 by default when the version supports
                    // it — most bare-metal Assisted Installer clusters are
                    // x86_64 — falling back to whatever architecture the
                    // Assisted Service lists first otherwise.
                    cpuArchitecture: preferred.cpuArchitectures.includes(
                      "x86_64",
                    )
                      ? "x86_64"
                      : preferred.cpuArchitectures[0] || prev.cpuArchitecture,
                  },
            );
          }
        })
        .catch((err) => {
          setOcpVersionsError(
            err instanceof Error
              ? err.message
              : "Failed to fetch supported OpenShift versions",
          );
        })
        .finally(() => setOcpVersionsLoading(false));
    },
    [],
  );

  const handleCancel = useCallback(() => {
    if (onClose) {
      onClose();
    } else {
      clusters.navigate();
    }
  }, [onClose, clusters]);

  const handleFinish = useCallback(() => {
    if (onSetupNext) {
      onSetupNext();
    } else if (onClose) {
      onClose();
    } else {
      clusters.navigate(formData.clusterName.trim());
    }
  }, [formData.clusterName, onSetupNext, onClose, clusters]);

  const handleGenerateOrFinish = useCallback(async () => {
    if (isoUrl) {
      handleFinish();
      return;
    }

    if (!ocmSessionId) {
      setError("Red Hat sign-in session expired. Please sign in again.");
      return;
    }

    setGenerating(true);
    setError(null);

    const clusterName = formData.clusterName.trim();

    try {
      const cluster = await createCluster(ocmSessionId, {
        name: clusterName,
        openshiftVersion: formData.ocpVersion,
        baseDnsDomain: formData.baseDomain,
        cpuArchitecture: formData.cpuArchitecture,
        sshPublicKey: formData.sshPublicKey || undefined,
        topology: formData.topology,
      });

      const infraEnv = await createInfraEnv(ocmSessionId, {
        name: `${clusterName}-infra-env`,
        clusterId: cluster.id,
        sshAuthorizedKey: formData.sshPublicKey || undefined,
        cpuArchitecture: formData.cpuArchitecture,
      });

      const { url } = await getImageUrl(ocmSessionId, infraEnv.id);

      setAssistedClusterId(cluster.id);
      setInfraEnvId(infraEnv.id);
      setIsoUrl(url);

      // Best-effort: register the cluster in FleetShift's own inventory
      // so it shows up in the Clusters list immediately. ISO generation
      // itself has already succeeded at this point, so a failure here
      // is surfaced but doesn't block the user from downloading the ISO.
      try {
        await registerFleetShiftCluster(clusterName, {
          releaseVersion: formData.ocpVersion,
          baseDomain: formData.baseDomain,
          cpuArchitecture: formData.cpuArchitecture,
          assistedClusterId: cluster.id,
          infraEnvId: infraEnv.id,
          fips: formData.compliance.fips,
          disconnected: formData.compliance.disconnected,
          topology: formData.topology,
        });
      } catch (registerErr) {
        // eslint-disable-next-line no-console
        console.error(
          "Failed to register Assisted cluster with FleetShift:",
          registerErr,
        );
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setGenerating(false);
    }
  }, [
    formData.baseDomain,
    formData.clusterName,
    formData.compliance.disconnected,
    formData.compliance.fips,
    formData.cpuArchitecture,
    formData.ocpVersion,
    formData.sshPublicKey,
    formData.topology,
    handleFinish,
    isoUrl,
    ocmSessionId,
  ]);

  const isClusterDetailsValid =
    CLUSTER_NAME_PATTERN.test(formData.clusterName.trim()) &&
    formData.ocpVersion.trim().length > 0 &&
    formData.baseDomain.trim().length > 0;

  return (
    <>
      {error && (
        <Alert
          variant="danger"
          title="ISO generation failed"
          isInline
          className="pf-v6-u-mb-md"
          actionClose={
            <button
              className="pf-v6-c-alert__action-close"
              onClick={() => setError(null)}
            />
          }
        >
          {error}
        </Alert>
      )}
      <Wizard
        title="Create Assisted Service Cluster"
        onClose={handleCancel}
        height={600}
        isVisitRequired
      >
        <WizardStep
          name="Red Hat Account"
          id="assisted-redhat-auth"
          footer={{ isNextDisabled: !ocmSessionId }}
        >
          <RedHatAuthStep onAuthenticated={handleRedHatAuthenticated} />
        </WizardStep>

        <WizardStep
          name="Welcome"
          id="assisted-welcome"
          isDisabled={!ocmSessionId}
        >
          <WelcomeStep />
        </WizardStep>

        <WizardStep
          name="Cluster details"
          id="assisted-cluster-details"
          status={isClusterDetailsValid ? "default" : "error"}
          isDisabled={generating || !ocmSessionId}
          footer={{ isNextDisabled: !isClusterDetailsValid }}
        >
          <ClusterDetailsStep
            formData={formData}
            onChange={updateField}
            ocpVersions={ocpVersions}
            ocpVersionsLoading={ocpVersionsLoading}
            ocpVersionsError={ocpVersionsError}
          />
        </WizardStep>

        <WizardStep
          name="Installation options"
          id="assisted-install-options"
          isDisabled={generating || !ocmSessionId}
        >
          <InstallationOptionsStep
            formData={formData}
            onChange={updateField}
          />
        </WizardStep>

        <WizardStep
          name="Review"
          id="assisted-review"
          isDisabled={generating || !ocmSessionId}
        >
          <ReviewStep formData={formData} ocmAccount={ocmAccount} />
        </WizardStep>

        <WizardStep
          name="Generate ISO"
          id="assisted-generate"
          isDisabled={generating || !ocmSessionId}
          footer={{
            nextButtonText: generating
              ? "Generating..."
              : isoUrl
                ? "Finish"
                : "Generate ISO",
            onNext: handleGenerateOrFinish,
            isNextDisabled: generating,
          }}
        >
          <GenerateIsoStep
            formData={formData}
            generating={generating}
            isoUrl={isoUrl}
            assistedClusterId={assistedClusterId}
            infraEnvId={infraEnvId}
          />
        </WizardStep>
      </Wizard>
    </>
  );
}
