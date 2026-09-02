import {
  Alert,
  Button,
  Content,
  EmptyState,
  EmptyStateActions,
  EmptyStateBody,
  EmptyStateFooter,
  Spinner,
  Stack,
  StackItem,
  Title,
} from "@patternfly/react-core";
import { CheckCircleIcon, ExternalLinkAltIcon } from "@patternfly/react-icons";
import { useEffect } from "react";

import type { OcmAccount } from "./api";
import { useRedHatDeviceAuth } from "./useRedHatDeviceAuth";

interface RedHatAuthStepProps {
  onAuthenticated: (sessionId: string, account: OcmAccount | null) => void;
}

export default function RedHatAuthStep({
  onAuthenticated,
}: RedHatAuthStepProps) {
  const { state, start, reset } = useRedHatDeviceAuth();

  useEffect(() => {
    if (state.status === "complete") {
      onAuthenticated(state.sessionId, state.account);
    }
    // Only re-fire when the session transitions to complete for a new
    // session ID — onAuthenticated is expected to be referentially stable
    // enough via the parent's useCallback, but we key on sessionId to be
    // safe against re-renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.status === "complete" ? state.sessionId : null]);

  if (state.status === "idle") {
    return (
      <EmptyState headingLevel="h2" titleText="Sign in with Red Hat">
        <EmptyStateBody>
          Provisioning bare-metal clusters through the Assisted Installer
          Service requires signing in with your Red Hat account. We&apos;ll use
          it to fetch supported OpenShift versions and generate your Discovery
          ISO.
        </EmptyStateBody>
        <EmptyStateFooter>
          <EmptyStateActions>
            <Button variant="primary" onClick={start}>
              Sign in with Red Hat
            </Button>
          </EmptyStateActions>
        </EmptyStateFooter>
      </EmptyState>
    );
  }

  if (state.status === "starting") {
    return (
      <EmptyState
        headingLevel="h2"
        titleText="Starting Red Hat sign-in..."
        icon={Spinner}
      >
        <EmptyStateBody>
          Contacting Red Hat SSO to start a device sign-in.
        </EmptyStateBody>
      </EmptyState>
    );
  }

  if (state.status === "pending") {
    return (
      <Stack hasGutter>
        <StackItem>
          <Title headingLevel="h3">Complete sign-in in your browser</Title>
        </StackItem>
        <StackItem>
          <Content component="p">
            Open the link below (or go to{" "}
            <strong>{state.verificationUri}</strong>) and confirm this code:
          </Content>
          <Title headingLevel="h1" className="pf-v6-u-my-md">
            {state.userCode}
          </Title>
          <Button
            variant="primary"
            icon={<ExternalLinkAltIcon />}
            component="a"
            href={state.verificationUriComplete || state.verificationUri}
            target="_blank"
            rel="noreferrer"
          >
            Open Red Hat sign-in
          </Button>
        </StackItem>
        <StackItem>
          <Content component="small">
            <Spinner size="sm" className="pf-v6-u-mr-sm" />
            Waiting for you to complete sign-in...
          </Content>
        </StackItem>
        <StackItem>
          <Button variant="link" isInline onClick={reset}>
            Cancel
          </Button>
        </StackItem>
      </Stack>
    );
  }

  if (state.status === "complete") {
    return (
      <EmptyState
        headingLevel="h2"
        titleText={
          state.account
            ? `Signed in as ${state.account.username}`
            : "Signed in to Red Hat"
        }
        icon={CheckCircleIcon}
        status="success"
      >
        <EmptyStateBody>
          {state.account?.email ? `Connected as ${state.account.email}. ` : ""}
          Click <strong>Next</strong> to continue.
        </EmptyStateBody>
      </EmptyState>
    );
  }

  if (state.status === "expired") {
    return (
      <Stack hasGutter>
        <StackItem>
          <Alert variant="warning" isInline title="Sign-in code expired">
            You didn&apos;t complete sign-in before the code expired. Try again.
          </Alert>
        </StackItem>
        <StackItem>
          <Button variant="primary" onClick={start}>
            Sign in with Red Hat
          </Button>
        </StackItem>
      </Stack>
    );
  }

  return (
    <Stack hasGutter>
      <StackItem>
        <Alert variant="danger" isInline title="Red Hat sign-in failed">
          {state.message}
        </Alert>
      </StackItem>
      <StackItem>
        <Button variant="primary" onClick={start}>
          Try again
        </Button>
      </StackItem>
    </Stack>
  );
}
