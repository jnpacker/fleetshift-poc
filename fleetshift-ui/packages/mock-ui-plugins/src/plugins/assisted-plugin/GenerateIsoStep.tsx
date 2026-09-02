import {
  Button,
  ClipboardCopy,
  Content,
  DescriptionList,
  DescriptionListDescription,
  DescriptionListGroup,
  DescriptionListTerm,
  EmptyState,
  EmptyStateActions,
  EmptyStateBody,
  EmptyStateFooter,
  Spinner,
  Stack,
  StackItem,
  Title,
} from "@patternfly/react-core";
import { CheckCircleIcon, DownloadIcon } from "@patternfly/react-icons";

import type { AssistedFormData } from "./CreateAssistedWizard";

interface GenerateIsoStepProps {
  formData: AssistedFormData;
  generating: boolean;
  isoUrl: string | null;
  assistedClusterId: string | null;
  infraEnvId: string | null;
}

export default function GenerateIsoStep({
  formData,
  generating,
  isoUrl,
  assistedClusterId,
  infraEnvId,
}: GenerateIsoStepProps) {
  if (generating) {
    return (
      <EmptyState
        headingLevel="h2"
        titleText="Generating Discovery ISO..."
        icon={Spinner}
      >
        <EmptyStateBody>
          Creating the cluster definition and InfraEnv for{" "}
          <strong>{formData.clusterName || "your cluster"}</strong> on the
          real Assisted Installer Service, then generating the Discovery ISO.
        </EmptyStateBody>
      </EmptyState>
    );
  }

  if (isoUrl) {
    return (
      <Stack hasGutter>
        <StackItem>
          <EmptyState
            headingLevel="h2"
            titleText="Discovery ISO ready"
            icon={CheckCircleIcon}
            status="success"
          >
            <EmptyStateBody>
              Boot your bare-metal hosts with the Discovery ISO below. Hosts
              check in with the Assisted Installer Service directly — they
              need network access to Red Hat&apos;s cloud (
              <code>api.openshift.com</code>), not to FleetShift. Once booted,
              hosts appear automatically on the cluster&apos;s Hosts tab.
            </EmptyStateBody>
            <EmptyStateFooter>
              <EmptyStateActions>
                <Button
                  variant="primary"
                  icon={<DownloadIcon />}
                  component="a"
                  href={isoUrl}
                  target="_blank"
                  rel="noreferrer"
                >
                  Download Discovery ISO
                </Button>
              </EmptyStateActions>
            </EmptyStateFooter>
          </EmptyState>
        </StackItem>
        <StackItem>
          <DescriptionList isCompact isHorizontal>
            <DescriptionListGroup>
              <DescriptionListTerm>Assisted cluster ID</DescriptionListTerm>
              <DescriptionListDescription>
                <ClipboardCopy
                  hoverTip="Copy"
                  clickTip="Copied"
                  isReadOnly
                  variant="inline-compact"
                >
                  {assistedClusterId || "—"}
                </ClipboardCopy>
              </DescriptionListDescription>
            </DescriptionListGroup>
            <DescriptionListGroup>
              <DescriptionListTerm>InfraEnv ID</DescriptionListTerm>
              <DescriptionListDescription>
                <ClipboardCopy
                  hoverTip="Copy"
                  clickTip="Copied"
                  isReadOnly
                  variant="inline-compact"
                >
                  {infraEnvId || "—"}
                </ClipboardCopy>
              </DescriptionListDescription>
            </DescriptionListGroup>
          </DescriptionList>
        </StackItem>
      </Stack>
    );
  }

  return (
    <Stack hasGutter>
      <StackItem>
        <Title headingLevel="h3">Generate Discovery ISO</Title>
        <Content component="p">
          Click <strong>Generate ISO</strong> below to create the cluster
          definition and Discovery ISO for{" "}
          <strong>{formData.clusterName || "your cluster"}</strong> on the
          real Assisted Installer Service.
        </Content>
        <Content component="small">
          Host discovery, validation, role assignment, and installation
          progress appear on the cluster&apos;s Hosts tab once hosts boot from
          the ISO and check in.
        </Content>
      </StackItem>
    </Stack>
  );
}
