import { useCallback, useEffect, useRef, useState } from "react";

import type { OcmAccount } from "./api";
import { getOcmAccount, pollDeviceAuth, startDeviceAuth } from "./api";

// Drives the Red Hat SSO device-authorization-grant flow (RFC 8628) for
// the assisted-plugin wizard's first step. The actual OAuth exchange and
// all calls to sso.redhat.com / api.openshift.com happen server-side (see
// fleetshift-server/internal/transport/http/assisted_ocm.go) — this hook
// only starts the flow, polls for completion on a recursive setTimeout
// (matching the signing-plugin's GitHub key polling pattern), and surfaces
// state to the wizard.

export type RedHatAuthState =
  | { status: "idle" }
  | { status: "starting" }
  | {
      status: "pending";
      sessionId: string;
      userCode: string;
      verificationUri: string;
      verificationUriComplete: string;
    }
  | { status: "complete"; sessionId: string; account: OcmAccount | null }
  | { status: "expired" }
  | { status: "error"; message: string };

const DEFAULT_POLL_INTERVAL_SECONDS = 5;

export function useRedHatDeviceAuth() {
  const [state, setState] = useState<RedHatAuthState>({ status: "idle" });
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cancelledRef = useRef(false);

  useEffect(
    () => () => {
      cancelledRef.current = true;
      if (timerRef.current) clearTimeout(timerRef.current);
    },
    [],
  );

  const schedulePoll = useCallback(
    (sessionId: string, intervalSeconds: number) => {
      timerRef.current = setTimeout(async () => {
        if (cancelledRef.current) return;
        try {
          const res = await pollDeviceAuth(sessionId);
          if (cancelledRef.current) return;

          if (res.status === "pending") {
            schedulePoll(sessionId, res.interval ?? intervalSeconds);
            return;
          }
          if (res.status === "complete") {
            let account: OcmAccount | null = null;
            try {
              account = await getOcmAccount(sessionId);
            } catch {
              // Non-fatal — the sign-in itself succeeded even if we
              // couldn't fetch account details for display.
            }
            if (!cancelledRef.current) {
              setState({ status: "complete", sessionId, account });
            }
            return;
          }
          if (res.status === "expired") {
            setState({ status: "expired" });
            return;
          }
          setState({
            status: "error",
            message: res.error || "Red Hat sign-in failed",
          });
        } catch {
          // Transient network error talking to our own backend — keep
          // polling rather than failing the whole flow on one blip.
          schedulePoll(sessionId, intervalSeconds);
        }
      }, intervalSeconds * 1000);
    },
    [],
  );

  const start = useCallback(async () => {
    setState({ status: "starting" });
    try {
      const resp = await startDeviceAuth();
      if (cancelledRef.current) return;
      setState({
        status: "pending",
        sessionId: resp.sessionId,
        userCode: resp.userCode,
        verificationUri: resp.verificationUri,
        verificationUriComplete: resp.verificationUriComplete,
      });
      schedulePoll(
        resp.sessionId,
        resp.interval || DEFAULT_POLL_INTERVAL_SECONDS,
      );
    } catch (err) {
      if (!cancelledRef.current) {
        setState({
          status: "error",
          message:
            err instanceof Error
              ? err.message
              : "Failed to start Red Hat sign-in",
        });
      }
    }
  }, [schedulePoll]);

  const reset = useCallback(() => {
    if (timerRef.current) clearTimeout(timerRef.current);
    setState({ status: "idle" });
  }, []);

  return { state, start, reset };
}
