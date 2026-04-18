import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PaperDetail } from "./PaperDetail";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("PaperDetail", () => {
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    globalThis.fetch = vi.fn<typeof fetch>();
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("shows a placeholder when no paper is focused", () => {
    render(<PaperDetail id={null} />);
    expect(screen.getByText(/Click a node/)).toBeInTheDocument();
  });

  it("loads and renders paper metadata including DOI link", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(
      json({
        paperId: "p1",
        title: "Attention Is All You Need",
        abstract: "We propose a new simple network architecture…",
        year: 2017,
        venue: "NeurIPS",
        authors: [{ name: "Ashish Vaswani" }],
        citationCount: 120000,
        referenceCount: 40,
        externalIds: { DOI: "10.1234/attn" },
        url: "https://www.semanticscholar.org/paper/p1",
      }),
    );
    render(<PaperDetail id="p1" />);
    await waitFor(() => {
      expect(
        screen.getByRole("heading", { name: /Attention Is All You Need/i }),
      ).toBeInTheDocument();
    });
    expect(screen.getByText(/NeurIPS/)).toBeInTheDocument();
    expect(screen.getByText(/120,000 citations/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /DOI/i })).toHaveAttribute(
      "href",
      "https://doi.org/10.1234/attn",
    );
  });

  it("surfaces a backend error", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(
      json({ error: "paper not found" }, 404),
    );
    render(<PaperDetail id="missing" />);
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("paper not found");
    });
  });
});
