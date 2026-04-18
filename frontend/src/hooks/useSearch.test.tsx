import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSearch } from "./useSearch";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("useSearch", () => {
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    globalThis.fetch = vi.fn<typeof fetch>();
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("transitions idle → loading → success", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(
      json({ total: 1, results: [{ id: "p1", title: "T" }] }),
    );
    const { result } = renderHook(() => useSearch());
    expect(result.current.state.status).toBe("idle");
    act(() => {
      void result.current.runSearch("transformers");
    });
    expect(result.current.state.status).toBe("loading");
    await waitFor(() => {
      expect(result.current.state.status).toBe("success");
    });
    if (result.current.state.status === "success") {
      expect(result.current.state.data.total).toBe(1);
      expect(result.current.state.query).toBe("transformers");
    }
  });

  it("captures backend errors", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(
      json({ error: "q required" }, 400),
    );
    const { result } = renderHook(() => useSearch());
    await act(async () => {
      await result.current.runSearch("x");
    });
    expect(result.current.state.status).toBe("error");
    if (result.current.state.status === "error") {
      expect(result.current.state.error).toBe("q required");
    }
  });
});
