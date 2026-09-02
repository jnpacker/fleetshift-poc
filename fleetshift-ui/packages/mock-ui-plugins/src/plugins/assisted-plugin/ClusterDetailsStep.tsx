import {
  Alert,
  Form,
  FormGroup,
  FormHelperText,
  FormSelect,
  FormSelectOption,
  HelperText,
  HelperTextItem,
  Spinner,
  TextArea,
  TextInput,
} from "@patternfly/react-core";

import type { OpenShiftVersionOption } from "./api";
import { minimumHostsForTopology, TOPOLOGY_OPTIONS } from "./api";
import type { AssistedFormData } from "./CreateAssistedWizard";

const CLUSTER_NAME_PATTERN = /^[a-z][-a-z0-9]*$/;

const FALLBACK_CPU_ARCHITECTURES = ["x86_64", "aarch64", "arm64", "ppc64le", "s390x"];

/**
 * x86_64 is by far the most common architecture for bare-metal Assisted
 * Installer clusters, so it should always appear first in the dropdown
 * (and thus be the default selection) regardless of the order the
 * Assisted Service reports supported architectures in for a given
 * OpenShift version.
 */
function sortArchitecturesX86First(architectures: string[]): string[] {
  if (!architectures.includes("x86_64")) {
    return architectures;
  }
  return ["x86_64", ...architectures.filter((arch) => arch !== "x86_64")];
}

interface ClusterDetailsStepProps {
  formData: AssistedFormData;
  onChange: <K extends keyof AssistedFormData>(
    field: K,
    value: AssistedFormData[K],
  ) => void;
  /** Real supported-versions list from Assisted Service, post sign-in. */
  ocpVersions: OpenShiftVersionOption[];
  ocpVersionsLoading: boolean;
  ocpVersionsError: string | null;
}

export default function ClusterDetailsStep({
  formData,
  onChange,
  ocpVersions,
  ocpVersionsLoading,
  ocpVersionsError,
}: ClusterDetailsStepProps) {
  const trimmedName = formData.clusterName.trim();
  const namePatternInvalid =
    trimmedName.length > 0 && !CLUSTER_NAME_PATTERN.test(trimmedName);
  const nameValidated = !trimmedName
    ? "default"
    : namePatternInvalid
      ? "error"
      : "default";

  const selectedVersion = ocpVersions.find(
    (v) => v.version === formData.ocpVersion,
  );
  const cpuArchitectureOptions = sortArchitecturesX86First(
    selectedVersion?.cpuArchitectures && selectedVersion.cpuArchitectures.length > 0
      ? selectedVersion.cpuArchitectures
      : FALLBACK_CPU_ARCHITECTURES,
  );

  return (
    <Form>
      <FormGroup
        label="Cluster name"
        isRequired
        fieldId="assisted-cluster-name"
      >
        <TextInput
          id="assisted-cluster-name"
          isRequired
          value={formData.clusterName}
          onChange={(_e, val) => onChange("clusterName", val)}
          placeholder="my-bare-metal-cluster"
          validated={nameValidated}
        />
        <FormHelperText>
          <HelperText>
            <HelperTextItem
              variant={nameValidated === "error" ? "error" : "default"}
            >
              {namePatternInvalid
                ? "Must start with a lowercase letter and contain only lowercase letters, digits, and hyphens."
                : "Lowercase letters, digits, and hyphens."}
            </HelperTextItem>
          </HelperText>
        </FormHelperText>
      </FormGroup>

      <FormGroup
        label="OpenShift version"
        isRequired
        fieldId="assisted-ocp-version"
      >
        {ocpVersionsError ? (
          <Alert
            variant="warning"
            isInline
            title="Failed to fetch supported OpenShift versions"
          >
            {ocpVersionsError}
          </Alert>
        ) : ocpVersionsLoading ? (
          <HelperText>
            <HelperTextItem icon={<Spinner size="sm" />}>
              Fetching supported versions from Assisted Service...
            </HelperTextItem>
          </HelperText>
        ) : (
          <FormSelect
            id="assisted-ocp-version"
            value={formData.ocpVersion}
            onChange={(_e, val) => onChange("ocpVersion", val)}
            validated={formData.ocpVersion.trim() ? "default" : "error"}
          >
            <FormSelectOption
              key=""
              value=""
              label="Select an OpenShift version"
              isDisabled
            />
            {ocpVersions.map((v) => (
              <FormSelectOption
                key={v.version}
                value={v.version}
                label={
                  v.default ? `${v.displayName} (recommended)` : v.displayName
                }
              />
            ))}
          </FormSelect>
        )}
      </FormGroup>

      <FormGroup label="Topology" isRequired fieldId="assisted-topology">
        <FormSelect
          id="assisted-topology"
          value={formData.topology}
          onChange={(_e, val) =>
            onChange("topology", val as AssistedFormData["topology"])
          }
        >
          {TOPOLOGY_OPTIONS.map((opt) => (
            <FormSelectOption
              key={opt.value}
              value={opt.value}
              label={opt.label}
            />
          ))}
        </FormSelect>
        <FormHelperText>
          <HelperText>
            <HelperTextItem>
              {
                TOPOLOGY_OPTIONS.find((opt) => opt.value === formData.topology)
                  ?.description
              }{" "}
              Requires at least {minimumHostsForTopology(formData.topology)}{" "}
              host
              {minimumHostsForTopology(formData.topology) > 1 ? "s" : ""}.
            </HelperTextItem>
          </HelperText>
        </FormHelperText>
      </FormGroup>

      <FormGroup label="Base domain" isRequired fieldId="assisted-base-domain">
        <TextInput
          id="assisted-base-domain"
          isRequired
          value={formData.baseDomain}
          onChange={(_e, val) => onChange("baseDomain", val)}
          placeholder="example.com"
          validated={formData.baseDomain.trim() ? "default" : "error"}
        />
      </FormGroup>

      <FormGroup
        label="CPU architecture"
        isRequired
        fieldId="assisted-cpu-architecture"
      >
        <FormSelect
          id="assisted-cpu-architecture"
          value={formData.cpuArchitecture}
          onChange={(_e, val) => onChange("cpuArchitecture", val)}
        >
          {cpuArchitectureOptions.map((arch) => (
            <FormSelectOption key={arch} value={arch} label={arch} />
          ))}
        </FormSelect>
      </FormGroup>

      <FormGroup label="SSH public key" fieldId="assisted-ssh-public-key">
        <TextArea
          id="assisted-ssh-public-key"
          value={formData.sshPublicKey}
          onChange={(_e, val) => onChange("sshPublicKey", val)}
          placeholder="ssh-ed25519 AAAA..."
          rows={3}
          aria-label="SSH public key"
        />
        <FormHelperText>
          <HelperText>
            <HelperTextItem>
              Optional — enables SSH access to hosts during discovery for
              debugging.
            </HelperTextItem>
          </HelperText>
        </FormHelperText>
      </FormGroup>
    </Form>
  );
}
