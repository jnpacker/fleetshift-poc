import {
  DescriptionList,
  DescriptionListDescription,
  DescriptionListGroup,
  DescriptionListTerm,
  Label,
  LabelGroup,
  Stack,
  StackItem,
  Title,
} from "@patternfly/react-core";
import type { ReactNode } from "react";

import type { OcmAccount } from "./api";
import { topologyLabel } from "./api";
import type { AssistedFormData } from "./CreateAssistedWizard";

interface ReviewStepProps {
  formData: AssistedFormData;
  ocmAccount: OcmAccount | null;
}

interface ReviewField {
  term: string;
  description: ReactNode;
}

function buildReviewFields(
  formData: AssistedFormData,
  ocmAccount: OcmAccount | null,
): ReviewField[] {
  const activeCompliance = (
    Object.entries(formData.compliance) as [
      keyof AssistedFormData["compliance"],
      boolean,
    ][]
  )
    .filter(([, enabled]) => enabled)
    .map(([key]) => key);

  return [
    {
      term: "Red Hat account",
      description: ocmAccount?.username || "Signed in",
    },
    { term: "Cluster name", description: formData.clusterName || "—" },
    { term: "OpenShift version", description: formData.ocpVersion || "—" },
    { term: "Base domain", description: formData.baseDomain || "—" },
    { term: "Topology", description: topologyLabel(formData.topology) },
    {
      term: "CPU architecture",
      description: formData.cpuArchitecture || "—",
    },
    {
      term: "SSH public key",
      description: formData.sshPublicKey
        ? `${formData.sshPublicKey.trim().slice(0, 40)}...`
        : "Not provided",
    },
    {
      term: "Installation options",
      description: activeCompliance.length ? (
        <LabelGroup>
          {activeCompliance.map((key) => (
            <Label key={key} isCompact color="green">
              {key}
            </Label>
          ))}
        </LabelGroup>
      ) : (
        "None selected"
      ),
    },
  ];
}

export default function ReviewStep({ formData, ocmAccount }: ReviewStepProps) {
  const fields = buildReviewFields(formData, ocmAccount);

  return (
    <Stack hasGutter>
      <StackItem>
        <Title headingLevel="h3">Review your configuration</Title>
      </StackItem>
      <StackItem>
        <DescriptionList isHorizontal>
          {fields.map((field) => (
            <DescriptionListGroup key={field.term}>
              <DescriptionListTerm>{field.term}</DescriptionListTerm>
              <DescriptionListDescription>
                {field.description}
              </DescriptionListDescription>
            </DescriptionListGroup>
          ))}
        </DescriptionList>
      </StackItem>
    </Stack>
  );
}
