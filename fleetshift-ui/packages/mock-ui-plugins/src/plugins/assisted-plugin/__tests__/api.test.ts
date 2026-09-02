import { afterEach, describe, expect, it, vi } from "vitest";

import type { CreateClusterRequest } from "../api";
import {
  createCluster,
  minimumHostsForTopology,
  topologyLabel,
} from "../api";

function mockFetch(body: unknown, status = 200) {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : "Error",
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as Response);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("createCluster", () => {
  const clusterResponse = {
    id: "cluster-1",
    name: "my-cluster",
    openshiftVersion: "4.19.10",
    status: "insufficient",
  };

  it("forwards the requested topology to the backend", async () => {
    const spy = mockFetch(clusterResponse);

    const req: CreateClusterRequest = {
      name: "my-cluster",
      openshiftVersion: "4.19.10",
      topology: "SNO",
    };

    await createCluster("session-1", req);

    expect(spy).toHaveBeenCalledOnce();
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe("/api/ui/assisted/clusters/session-1");
    expect(JSON.parse(init?.body as string)).toEqual(req);
  });

  it("omits topology when not specified, leaving the server default to apply", async () => {
    const spy = mockFetch(clusterResponse);

    await createCluster("session-1", {
      name: "other-cluster",
      openshiftVersion: "4.19.10",
    });

    const body = JSON.parse(spy.mock.calls[0][1]?.body as string);
    expect(body.topology).toBeUndefined();
  });
});

describe("minimumHostsForTopology", () => {
  it("requires exactly 1 host for SNO", () => {
    expect(minimumHostsForTopology("SNO")).toBe(1);
  });

  it("requires 3 hosts for Compact", () => {
    expect(minimumHostsForTopology("Compact")).toBe(3);
  });

  it("requires 3 hosts for Full", () => {
    expect(minimumHostsForTopology("Full")).toBe(3);
  });
});

describe("topologyLabel", () => {
  it("returns the display label for a known topology", () => {
    expect(topologyLabel("SNO")).toBe("Single Node OpenShift (SNO)");
  });

  it("falls back to the raw value for unknown/legacy topologies", () => {
    expect(topologyLabel("")).toBe("");
    expect(topologyLabel("Unknown")).toBe("Unknown");
  });
});
