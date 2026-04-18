import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Legend } from "./Legend";

describe("Legend", () => {
  it("renders edge legend rows", () => {
    render(<Legend />);
    expect(screen.getByText(/A cites B/i)).toBeInTheDocument();
    expect(screen.getByText(/Similarity link/i)).toBeInTheDocument();
  });

  it("shows the given year range", () => {
    render(<Legend yearRange={[1995, 2024]} />);
    expect(screen.getByText("1995")).toBeInTheDocument();
    expect(screen.getByText("2024")).toBeInTheDocument();
  });
});
