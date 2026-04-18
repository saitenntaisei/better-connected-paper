import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ResultsList } from "./ResultsList";
import type { SearchResult } from "../types/api";

const items: SearchResult[] = [
  {
    id: "p1",
    title: "Attention Is All You Need",
    year: 2017,
    authors: ["Ashish Vaswani"],
    citationCount: 123456,
    venue: "NeurIPS",
  },
  { id: "p2", title: "BERT", year: 2018, authors: ["Devlin"] },
];

describe("ResultsList", () => {
  it("renders an empty state with no results", () => {
    render(<ResultsList results={[]} onSelect={() => {}} />);
    expect(screen.getByRole("status")).toHaveTextContent(/no results/i);
  });

  it("marks the selected entry and fires onSelect", async () => {
    const onSelect = vi.fn();
    render(<ResultsList results={items} selectedId="p1" onSelect={onSelect} />);
    const options = screen.getAllByRole("option");
    expect(options).toHaveLength(2);
    expect(options[0]).toHaveAttribute("aria-selected", "true");
    expect(options[1]).toHaveAttribute("aria-selected", "false");
    await userEvent.setup().click(options[1]);
    expect(onSelect).toHaveBeenCalledWith(items[1]);
  });
});
