import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GraphResponse } from "../types/api";

// Capture every cytoscape() call so we can assert on the elements/style.
const cyInstances: { opts: unknown; destroy: ReturnType<typeof vi.fn>; on: ReturnType<typeof vi.fn>; off: ReturnType<typeof vi.fn> }[] = [];

vi.mock("cytoscape", () => {
  const fn = vi.fn((opts: unknown) => {
    const instance = {
      opts,
      destroy: vi.fn(),
      on: vi.fn(),
      off: vi.fn(),
    };
    cyInstances.push(instance);
    return instance;
  });
  const mod = Object.assign(fn, { use: vi.fn() });
  return { default: mod };
});
vi.mock("cytoscape-cose-bilkent", () => ({ default: () => {} }));

const { Graph } = await import("./Graph");

const sample: GraphResponse = {
  seed: { id: "S", title: "Seed", similarity: 0, isSeed: true },
  nodes: [
    { id: "S", title: "Seed", year: 2017, similarity: 0, isSeed: true },
    { id: "A", title: "Ancestor", year: 2010, similarity: 0.5 },
  ],
  edges: [{ source: "S", target: "A", kind: "cite", weight: 1 }],
  builtAt: "2026-04-18T00:00:00Z",
};

describe("Graph", () => {
  afterEach(() => {
    cleanup();
    cyInstances.length = 0;
  });

  it("renders a container and mounts cytoscape with directed citation edges", () => {
    render(<Graph data={sample} />);
    expect(screen.getByTestId("graph")).toBeInTheDocument();
    expect(cyInstances).toHaveLength(1);
    const opts = cyInstances[0].opts as {
      elements: Array<{ group: string; classes?: string; data: { id?: string } }>;
      layout: { name: string };
    };
    expect(opts.layout.name).toBe("cose-bilkent");
    const edges = opts.elements.filter((e) => e.group === "edges");
    expect(edges).toHaveLength(1);
    expect(edges[0].classes).toBe("cite");
  });

  it("destroys the cytoscape instance on unmount", () => {
    const { unmount } = render(<Graph data={sample} />);
    const instance = cyInstances[0];
    unmount();
    expect(instance.destroy).toHaveBeenCalled();
  });

  it("routes node double-clicks and touch double-taps to onSeedChange", () => {
    const onSeedChange = vi.fn();
    render(<Graph data={sample} onSeedChange={onSeedChange} />);
    const instance = cyInstances[0];
    for (const ev of ["dblclick", "dbltap"]) {
      const call = instance.on.mock.calls.find(
        (args) => args[0] === ev && args[1] === "node",
      );
      expect(call, `expected ${ev} handler to be registered`).toBeDefined();
      const handler = call![2] as (evt: { target: { id: () => string } }) => void;
      onSeedChange.mockClear();
      handler({ target: { id: () => "A" } });
      expect(onSeedChange).toHaveBeenCalledWith("A");
    }
  });
});
