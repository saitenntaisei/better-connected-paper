import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./components/Graph", () => ({
  Graph: ({
    data,
    onSeedChange,
  }: {
    data: { seed: { title: string } };
    onSeedChange?: (id: string) => void;
  }) => (
    <div data-testid="graph-stub">
      graph:{data.seed.title}
      {onSeedChange && (
        <button
          type="button"
          data-testid="graph-dblclick-A"
          onClick={() => onSeedChange("A")}
        >
          reseed to A
        </button>
      )}
    </div>
  ),
}));

const { default: App } = await import("./App");

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("App", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("renders the app shell", () => {
    globalThis.fetch = vi.fn<typeof fetch>();
    render(<App />);
    expect(
      screen.getByRole("heading", { name: /Better Connected Paper/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toBeInTheDocument();
  });

  it("search → select → graph build", async () => {
    globalThis.fetch = vi
      .fn<typeof fetch>()
      .mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/search")) {
          return json({
            total: 1,
            results: [{ id: "p1", title: "Attention Is All You Need", year: 2017 }],
          });
        }
        if (url === "/api/graph/build") {
          return json({
            seed: { id: "p1", title: "Attention Is All You Need", similarity: 0, isSeed: true },
            nodes: [],
            edges: [],
            builtAt: "2026-04-18T00:00:00Z",
          });
        }
        if (url.startsWith("/api/paper/")) {
          return json({ paperId: "p1", title: "Attention Is All You Need" });
        }
        throw new Error(`unexpected fetch: ${url}`);
      });

    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByRole("searchbox"), "transformers");
    await user.click(screen.getByRole("button", { name: /search/i }));

    await waitFor(() => {
      expect(
        screen.getByRole("option", { name: /Attention Is All You Need/ }),
      ).toBeInTheDocument();
    });
    await user.click(screen.getByRole("option", { name: /Attention Is All You Need/ }));

    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toBeInTheDocument();
    });
    const urls = vi.mocked(globalThis.fetch).mock.calls.map((c) => String(c[0]));
    expect(urls).toContain("/api/graph/build");
    expect(new URL(window.location.href).searchParams.get("seed")).toBe("p1");
  });

  it("rebuilds graph when a node is double-clicked", async () => {
    const buildCalls: string[] = [];
    globalThis.fetch = vi
      .fn<typeof fetch>()
      .mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/graph/build") {
          const body = JSON.parse(String(init?.body ?? "{}")) as { seedId?: string };
          const seedId = body.seedId ?? "unknown";
          buildCalls.push(seedId);
          return json({
            seed: { id: seedId, title: `paper-${seedId}`, similarity: 0, isSeed: true },
            nodes: [],
            edges: [],
            builtAt: "2026-04-18T00:00:00Z",
          });
        }
        if (url.startsWith("/api/paper/")) {
          return json({ paperId: "x", title: "x" });
        }
        throw new Error(`unexpected fetch: ${url}`);
      });

    window.history.replaceState({}, "", "/?seed=first");
    const user = userEvent.setup();
    render(<App />);
    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toHaveTextContent("graph:paper-first");
    });

    await user.click(screen.getByTestId("graph-dblclick-A"));

    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toHaveTextContent("graph:paper-A");
    });
    expect(buildCalls).toEqual(["first", "A"]);
    expect(new URL(window.location.href).searchParams.get("seed")).toBe("A");
  });

  it("rebuilds graph when popstate advances the seed via URL", async () => {
    const buildCalls: string[] = [];
    globalThis.fetch = vi
      .fn<typeof fetch>()
      .mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/graph/build") {
          const body = JSON.parse(String(init?.body ?? "{}")) as { seedId?: string };
          const seedId = body.seedId ?? "unknown";
          buildCalls.push(seedId);
          return json({
            seed: { id: seedId, title: `paper-${seedId}`, similarity: 0, isSeed: true },
            nodes: [],
            edges: [],
            builtAt: "2026-04-18T00:00:00Z",
          });
        }
        if (url.startsWith("/api/paper/")) {
          return json({ paperId: "x", title: "x" });
        }
        throw new Error(`unexpected fetch: ${url}`);
      });

    window.history.replaceState({}, "", "/?seed=first");
    render(<App />);
    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toHaveTextContent("graph:paper-first");
    });

    await act(async () => {
      window.history.pushState({}, "", "/?seed=second");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });

    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toHaveTextContent("graph:paper-second");
    });
    expect(buildCalls).toEqual(["first", "second"]);
  });
});
