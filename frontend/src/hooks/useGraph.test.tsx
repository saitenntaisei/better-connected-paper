import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useGraph } from "./useGraph";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("useGraph cache", () => {
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    globalThis.fetch = vi.fn<typeof fetch>();
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("alias and canonical id resolve to the same payload after one fetch", async () => {
    let calls = 0;
    globalThis.fetch = vi
      .fn<typeof fetch>()
      .mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/graph/build") {
          calls += 1;
          return json({
            seed: { id: "W1", title: "paper", similarity: 0, isSeed: true },
            nodes: [],
            edges: [],
            builtAt: "2026-04-18T00:00:00Z",
          });
        }
        throw new Error(`unexpected fetch: ${url}`);
      });

    const { result } = renderHook(() => useGraph());

    await act(async () => {
      await result.current.build("doi:10.1/x");
    });
    await act(async () => {
      await result.current.build("W1");
    });

    expect(calls).toBe(1);
    expect(result.current.state).toMatchObject({
      status: "success",
      data: { seed: { id: "W1" } },
    });
  });

  it("a fresh rebuild from any alias refreshes the canonical entry for all aliases", async () => {
    let revision = 0;
    globalThis.fetch = vi
      .fn<typeof fetch>()
      .mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/graph/build") {
          revision += 1;
          return json({
            seed: {
              id: "W1",
              title: `revision ${revision}`,
              similarity: 0,
              isSeed: true,
            },
            nodes: [],
            edges: [],
            builtAt: `2026-04-18T00:00:0${revision}Z`,
          });
        }
        throw new Error(`unexpected fetch: ${url}`);
      });

    const { result } = renderHook(() => useGraph());

    // 1. Load via DOI alias → cached under canonical "W1".
    await act(async () => {
      await result.current.build("doi:10.1/x");
    });
    expect(result.current.state).toMatchObject({
      status: "success",
      data: { seed: { title: "revision 1" } },
    });

    // 2. Retry from the canonical id (fresh=true) — refetches.
    await act(async () => {
      await result.current.build("W1", true);
    });
    expect(result.current.state).toMatchObject({
      status: "success",
      data: { seed: { title: "revision 2" } },
    });

    // 3. Re-visit the DOI alias URL. Must restore the refreshed payload,
    //    not the stale "revision 1" the original alias entry held before.
    await act(async () => {
      await result.current.build("doi:10.1/x");
    });
    expect(result.current.state).toMatchObject({
      status: "success",
      data: { seed: { title: "revision 2" } },
    });
    expect(globalThis.fetch).toHaveBeenCalledTimes(2);
  });
});
