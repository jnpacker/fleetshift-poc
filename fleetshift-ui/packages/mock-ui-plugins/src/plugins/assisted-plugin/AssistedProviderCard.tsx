import type { ClusterProviderCardProps } from "@fleetshift/common";
import {
  Card,
  CardBody,
  CardHeader,
  CardTitle,
  Content,
  Icon,
  Split,
  SplitItem,
} from "@patternfly/react-core";

// Named export sized via 1em so it scales inside PatternFly's <Icon> wrapper.
export function AssistedIcon() {
  return (
    <svg
      viewBox="0 0 16 16"
      width="1em"
      height="1em"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      role="img"
      aria-hidden="true"
    >
      <rect x="1" y="1" width="14" height="3.2" rx="0.6" fill="currentColor" />
      <rect
        x="1"
        y="6.4"
        width="14"
        height="3.2"
        rx="0.6"
        fill="currentColor"
      />
      <rect
        x="1"
        y="11.8"
        width="14"
        height="3.2"
        rx="0.6"
        fill="currentColor"
      />
      <circle cx="3.2" cy="2.6" r="0.6" fill="white" />
      <circle cx="3.2" cy="8" r="0.6" fill="white" />
      <circle cx="3.2" cy="13.4" r="0.6" fill="white" />
    </svg>
  );
}

export default function AssistedProviderCard({
  onSelect,
}: ClusterProviderCardProps) {
  return (
    <Card isClickable isCompact>
      <CardHeader
        selectableActions={{
          onClickAction: onSelect,
          selectableActionAriaLabel: "Select Assisted Service provider",
        }}
      >
        <CardTitle>
          <Split hasGutter>
            <SplitItem>
              <Icon size="xl">
                <AssistedIcon />
              </Icon>
            </SplitItem>
            <SplitItem isFilled>Assisted Service</SplitItem>
          </Split>
        </CardTitle>
      </CardHeader>
      <CardBody>
        <Content component="p">
          Provision bare-metal OpenShift clusters using the Red Hat Assisted
          Installer Service.
        </Content>
      </CardBody>
    </Card>
  );
}
