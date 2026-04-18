import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SearchBar } from "./SearchBar";

describe("SearchBar", () => {
  it("submits trimmed query on enter and via button", async () => {
    const onSubmit = vi.fn();
    render(<SearchBar onSubmit={onSubmit} />);
    const input = screen.getByRole("searchbox");
    const user = userEvent.setup();

    await user.type(input, "  attention  ");
    await user.keyboard("{Enter}");
    expect(onSubmit).toHaveBeenLastCalledWith("attention");

    await user.click(screen.getByRole("button", { name: /search/i }));
    expect(onSubmit).toHaveBeenCalledTimes(2);
  });

  it("disables submission for blank input", async () => {
    const onSubmit = vi.fn();
    render(<SearchBar onSubmit={onSubmit} />);
    expect(screen.getByRole("button", { name: /search/i })).toBeDisabled();
    await userEvent.setup().type(screen.getByRole("searchbox"), "   ");
    expect(screen.getByRole("button", { name: /search/i })).toBeDisabled();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("shows a busy label when loading", () => {
    render(<SearchBar onSubmit={() => {}} initial="x" busy />);
    expect(screen.getByRole("button")).toHaveTextContent(/searching/i);
    expect(screen.getByRole("button")).toBeDisabled();
  });
});
