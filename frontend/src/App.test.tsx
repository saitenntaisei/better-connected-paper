import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";

describe("App", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: "ok", time: "2026-04-18T00:00:00Z" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("renders the app shell and reflects backend health", async () => {
    render(<App />);
    expect(screen.getByRole("heading", { name: /Better Connected Paper/i })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByTestId("backend-status")).toHaveTextContent(/ok/i);
    });
  });
});
