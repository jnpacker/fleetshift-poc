// Persists the Red Hat SSO OCM session ID (from the device-authorization
// flow) across page navigations, so the Hosts tab on a cluster's detail
// page can resume polling the Assisted Service without re-running the
// wizard's sign-in step every time. This is a client-side convenience
// only — the actual session and its tokens live server-side in the Go
// backend's in-memory ocmSessionStore (30 minute TTL); once that
// expires, callers fall back to prompting sign-in again (see
// HostDiscoveryTab.tsx).
const STORAGE_KEY = "fleetshift.assisted.ocmSessionId";

export function getStoredOcmSessionId(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

export function setStoredOcmSessionId(sessionId: string): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, sessionId);
  } catch {
    // Ignore — e.g. private browsing mode with storage disabled. The
    // wizard/tab still function, just without cross-page persistence.
  }
}

export function clearStoredOcmSessionId(): void {
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // See above.
  }
}
