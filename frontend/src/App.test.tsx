import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

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

  it("searches, lists results, and updates the selection panel on click", async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(
      json({
        total: 2,
        results: [
          { id: "p1", title: "Attention Is All You Need", year: 2017 },
          { id: "p2", title: "BERT", year: 2018 },
        ],
      }),
    );
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByRole("searchbox"), "transformers");
    await user.click(screen.getByRole("button", { name: /search/i }));

    await waitFor(() => {
      expect(screen.getByText(/Attention Is All You Need/i)).toBeInTheDocument();
    });

    await user.click(screen.getByRole("option", { name: /BERT/i }));
    expect(screen.getByRole("complementary")).toHaveTextContent(/BERT/i);
  });
});
