import { describe, expect, it } from "vitest";
import { nodeSize, toElements, yearColor } from "./graphElements";
import type { GraphResponse } from "../types/api";

describe("toElements", () => {
  it("maps nodes and directed citation edges to cytoscape elements", () => {
    const resp: GraphResponse = {
      seed: { id: "S", title: "Seed", similarity: 0, isSeed: true },
      nodes: [
        { id: "S", title: "Seed", year: 2017, similarity: 0, isSeed: true, citationCount: 90000 },
        { id: "A", title: "Ancestor", year: 2010, similarity: 0.8, citationCount: 1200 },
      ],
      edges: [
        { source: "S", target: "A", kind: "cite", weight: 1 },
        { source: "A", target: "S", kind: "similarity", weight: 0.7 },
      ],
      builtAt: "2026-04-18T00:00:00Z",
    };
    const { elements, yearRange } = toElements(resp);
    expect(yearRange).toEqual([2010, 2017]);
    const nodes = elements.filter((e) => e.group === "nodes");
    const edges = elements.filter((e) => e.group === "edges");
    expect(nodes).toHaveLength(2);
    expect(edges).toHaveLength(2);

    const seed = nodes.find((n) => n.data.id === "S")!;
    expect(seed.classes).toBe("seed");
    expect(seed.data.isSeed).toBe(true);

    const cite = edges.find((e) => e.classes === "cite")!;
    expect(cite.data).toMatchObject({ source: "S", target: "A", kind: "cite" });
    const sim = edges.find((e) => e.classes === "similarity")!;
    expect(sim.data.kind).toBe("similarity");
  });
});

describe("nodeSize", () => {
  it("is bounded and grows with citation count", () => {
    const small = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 0 });
    const big = nodeSize({ id: "b", title: "b", similarity: 0, citationCount: 100_000 });
    expect(small).toBeGreaterThanOrEqual(18);
    expect(big).toBeLessThanOrEqual(80);
    expect(big).toBeGreaterThan(small);
  });

  it("adds a premium to the seed node", () => {
    const regular = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 100 });
    const seed = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 100, isSeed: true });
    expect(seed).toBeGreaterThan(regular);
  });
});

describe("yearColor", () => {
  it("falls back to neutral grey for unknown years", () => {
    expect(yearColor(undefined, 2000, 2020)).toBe("#9ca3af");
  });
  it("produces distinct hues for earliest vs latest year", () => {
    const early = yearColor(2000, 2000, 2020);
    const late = yearColor(2020, 2000, 2020);
    expect(early).not.toEqual(late);
  });
});
