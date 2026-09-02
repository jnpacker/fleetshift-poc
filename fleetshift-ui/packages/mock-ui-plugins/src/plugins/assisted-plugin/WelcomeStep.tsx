import {
  Content,
  Flex,
  FlexItem,
  Icon,
  Stack,
  StackItem,
  Title,
} from "@patternfly/react-core";
import { CogIcon, LockIcon, RocketIcon } from "@patternfly/react-icons";
import type { ComponentType } from "react";

interface WelcomeHighlight {
  icon: ComponentType;
  title: string;
  description: string;
}

const HIGHLIGHTS: WelcomeHighlight[] = [
  {
    icon: RocketIcon,
    title: "Automated bootstrap",
    description:
      "Boot bare-metal hosts with a single Discovery ISO and let the Assisted Installer Service handle validation and installation.",
  },
  {
    icon: LockIcon,
    title: "Fully disconnected",
    description:
      "Provision air-gapped OpenShift clusters without leaving your network perimeter.",
  },
  {
    icon: CogIcon,
    title: "Smart defaults",
    description:
      "Sensible networking, storage, and platform defaults out of the box.",
  },
];

export default function WelcomeStep() {
  return (
    <Stack hasGutter>
      <StackItem>
        <Title headingLevel="h2">
          Provision bare-metal OpenShift with Assisted Service
        </Title>
        <Content component="p">
          This guided wizard walks you through creating a bare-metal OpenShift
          cluster using the Red Hat Assisted Installer Service — from cluster
          details to Discovery ISO generation.
        </Content>
      </StackItem>
      <StackItem>
        <Flex
          direction={{ default: "column", md: "row" }}
          gap={{ default: "gapMd" }}
        >
          {HIGHLIGHTS.map((highlight) => (
            <FlexItem key={highlight.title} flex={{ default: "flex_1" }}>
              <Stack hasGutter>
                <StackItem>
                  <Icon size="lg">
                    <highlight.icon />
                  </Icon>
                </StackItem>
                <StackItem>
                  <Title headingLevel="h4">{highlight.title}</Title>
                </StackItem>
                <StackItem>
                  <Content component="small">{highlight.description}</Content>
                </StackItem>
              </Stack>
            </FlexItem>
          ))}
        </Flex>
      </StackItem>
    </Stack>
  );
}
