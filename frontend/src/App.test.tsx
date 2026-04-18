import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./components/Graph", () => ({
  Graph: ({ data }: { data: { seed: { title: string } } }) => (
    <div data-testid="graph-stub">graph:{data.seed.title}</div>
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
    globalThis.fetch = vi.fn<typeof fetch>();
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("renders the app shell", () => {
    render(<App />);
    expect(
      screen.getByRole("heading", { name: /Better Connected Paper/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toBeInTheDocument();
  });

  it("search → select → graph build", async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(
      json({
        total: 1,
        results: [{ id: "p1", title: "Attention Is All You Need", year: 2017 }],
      }),
    );
    fetchMock.mockResolvedValueOnce(
      json({
        seed: {
          id: "p1",
          title: "Attention Is All You Need",
          similarity: 0,
          isSeed: true,
        },
        nodes: [],
        edges: [],
        builtAt: "2026-04-18T00:00:00Z",
      }),
    );
    fetchMock.mockResolvedValue(
      json({ paperId: "p1", title: "Attention Is All You Need" }),
    );

    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByRole("searchbox"), "transformers");
    await user.click(screen.getByRole("button", { name: /search/i }));

    await waitFor(() => {
      expect(screen.getByText(/Attention Is All You Need/)).toBeInTheDocument();
    });
    await user.click(screen.getByRole("option", { name: /Attention Is All You Need/ }));

    await waitFor(() => {
      expect(screen.getByTestId("graph-stub")).toBeInTheDocument();
    });
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls).toContain("/api/graph/build");
  });
});
