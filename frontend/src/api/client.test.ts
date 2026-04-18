import { describe, expect, it, vi } from "vitest";
import { ApiError, buildGraph, getHealth, search } from "./client";

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

describe("api client", () => {
  it("getHealth hits /api/health", async () => {
    const fetchSpy = vi
      .fn<typeof fetch>()
      .mockResolvedValue(jsonResponse({ status: "ok", time: "2026-04-18T00:00:00Z" }));
    const out = await getHealth({ fetch: fetchSpy });
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/health",
      expect.objectContaining({ method: "GET" }),
    );
    expect(out.status).toBe("ok");
  });

  it("search encodes the query string", async () => {
    const fetchSpy = vi
      .fn<typeof fetch>()
      .mockResolvedValue(jsonResponse({ total: 0, results: [] }));
    await search("attention mechanism", 5, { fetch: fetchSpy });
    const url = fetchSpy.mock.calls[0]?.[0];
    expect(String(url)).toBe("/api/search?q=attention+mechanism&limit=5");
  });

  it("buildGraph POSTs seedId in JSON body", async () => {
    const fetchSpy = vi.fn<typeof fetch>().mockResolvedValue(
      jsonResponse({
        seed: { id: "S", title: "T", similarity: 0 },
        nodes: [],
        edges: [],
        builtAt: "2026-04-18T00:00:00Z",
      }),
    );
    await buildGraph("S", true, { fetch: fetchSpy });
    const init = fetchSpy.mock.calls[0]?.[1];
    expect(init?.method).toBe("POST");
    expect(init?.body).toBe(JSON.stringify({ seedId: "S", fresh: true }));
  });

  it("throws ApiError carrying the backend error message", async () => {
    const fetchSpy = vi
      .fn<typeof fetch>()
      .mockResolvedValue(jsonResponse({ error: "seedId required" }, { status: 400 }));
    await expect(buildGraph("", false, { fetch: fetchSpy })).rejects.toMatchObject({
      name: "ApiError",
      status: 400,
      message: "seedId required",
    });
    await expect(buildGraph("", false, { fetch: fetchSpy })).rejects.toBeInstanceOf(
      ApiError,
    );
  });
});
