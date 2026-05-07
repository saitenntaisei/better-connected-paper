import { describe, expect, it } from "vitest";
import { nodeSize, similarityEdgeStyle, toElements, yearColor } from "./graphElements";
import type { GraphResponse } from "../types/api";

const sample: GraphResponse = {
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

describe("toElements", () => {
  it("defaults to cite mode and maps directed citation edges only", () => {
    const { elements, yearRange } = toElements(sample);
    expect(yearRange).toEqual([2010, 2017]);
    const nodes = elements.filter((e) => e.group === "nodes");
    const edges = elements.filter((e) => e.group === "edges");
    expect(nodes).toHaveLength(2);
    expect(edges).toHaveLength(1);
    expect(edges[0].classes).toBe("cite");

    const seed = nodes.find((n) => n.data.id === "S")!;
    expect(seed.classes).toBe("seed");
    expect(seed.data.isSeed).toBe(true);
  });

  it("emits only similarity edges when edgeMode is similarity", () => {
    const { elements } = toElements(sample, { edgeMode: "similarity" });
    const edges = elements.filter((e) => e.group === "edges");
    expect(edges).toHaveLength(1);
    expect(edges[0].classes).toBe("similarity");
    expect(edges[0].data).toMatchObject({ source: "A", target: "S", kind: "similarity" });
  });

  it("drops nodes that have no edges of the selected kind (cite mode)", () => {
    const graph: GraphResponse = {
      seed: { id: "S", title: "Seed", similarity: 0, isSeed: true },
      nodes: [
        { id: "S", title: "Seed", year: 2020, similarity: 0, isSeed: true, citationCount: 10 },
        { id: "A", title: "Cites S", year: 2021, similarity: 0.6, citationCount: 5 },
        { id: "B", title: "Only similar", year: 2022, similarity: 0.5, citationCount: 3 },
      ],
      edges: [
        { source: "A", target: "S", kind: "cite", weight: 1 },
        { source: "B", target: "S", kind: "similarity", weight: 0.4 },
      ],
      builtAt: "2026-04-19T00:00:00Z",
    };
    const { elements } = toElements(graph, { edgeMode: "cite" });
    const nodeIds = elements.filter((e) => e.group === "nodes").map((e) => e.data.id);
    expect(nodeIds.sort()).toEqual(["A", "S"]);
  });

  it("drops nodes that have no edges of the selected kind (similarity mode)", () => {
    const graph: GraphResponse = {
      seed: { id: "S", title: "Seed", similarity: 0, isSeed: true },
      nodes: [
        { id: "S", title: "Seed", year: 2020, similarity: 0, isSeed: true, citationCount: 10 },
        { id: "A", title: "Cites S only", year: 2021, similarity: 0.6, citationCount: 5 },
        { id: "B", title: "Similar to S", year: 2022, similarity: 0.5, citationCount: 3 },
      ],
      edges: [
        { source: "A", target: "S", kind: "cite", weight: 1 },
        { source: "B", target: "S", kind: "similarity", weight: 0.4 },
      ],
      builtAt: "2026-04-19T00:00:00Z",
    };
    const { elements } = toElements(graph, { edgeMode: "similarity" });
    const nodeIds = elements.filter((e) => e.group === "nodes").map((e) => e.data.id);
    expect(nodeIds.sort()).toEqual(["B", "S"]);
  });

  it("keeps the seed even when it has no edges of the selected kind", () => {
    const graph: GraphResponse = {
      seed: { id: "S", title: "Seed", similarity: 0, isSeed: true },
      nodes: [
        { id: "S", title: "Seed", year: 2020, similarity: 0, isSeed: true, citationCount: 10 },
        { id: "A", title: "A", year: 2021, similarity: 0.6, citationCount: 5 },
        { id: "B", title: "B", year: 2022, similarity: 0.5, citationCount: 3 },
      ],
      edges: [{ source: "A", target: "B", kind: "cite", weight: 1 }],
      builtAt: "2026-04-19T00:00:00Z",
    };
    const { elements } = toElements(graph, { edgeMode: "cite" });
    const nodeIds = elements.filter((e) => e.group === "nodes").map((e) => e.data.id);
    expect(nodeIds.sort()).toEqual(["A", "B", "S"]);
  });
});

describe("nodeSize", () => {
  it("is bounded and grows with citation count", () => {
    const small = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 0 });
    const big = nodeSize({ id: "b", title: "b", similarity: 0, citationCount: 100_000 });
    expect(small).toBeGreaterThanOrEqual(10);
    expect(big).toBeLessThanOrEqual(80);
    expect(big).toBeGreaterThan(small);
  });

  it("spreads visibly across the 0-100 citation range", () => {
    const zero = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 0 });
    const hundred = nodeSize({ id: "b", title: "b", similarity: 0, citationCount: 100 });
    expect(hundred - zero).toBeGreaterThan(30);
  });

  it("treats 10k+ citations as the biggest circle", () => {
    const tenK = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 10_000 });
    const hundredK = nodeSize({ id: "b", title: "b", similarity: 0, citationCount: 100_000 });
    expect(hundredK).toBe(tenK);
  });

  it("adds a premium to the seed node", () => {
    const regular = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 100 });
    const seed = nodeSize({ id: "a", title: "a", similarity: 0, citationCount: 100, isSeed: true });
    expect(seed).toBeGreaterThan(regular);
  });
});

describe("similarityEdgeStyle", () => {
  it("stretches a narrow weight range across the visible gradient", () => {
    const low = similarityEdgeStyle(0.08, 0.08, 0.27);
    const high = similarityEdgeStyle(0.27, 0.08, 0.27);
    expect(low.opacity).toBeLessThan(high.opacity);
    expect(low.width).toBeLessThan(high.width);
    expect(low.color).not.toEqual(high.color);
  });

  it("clamps out-of-range weights", () => {
    const underflow = similarityEdgeStyle(-0.5, 0, 1);
    const overflow = similarityEdgeStyle(2, 0, 1);
    expect(underflow.opacity).toBeCloseTo(0.55);
    expect(overflow.opacity).toBeCloseTo(0.95);
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
