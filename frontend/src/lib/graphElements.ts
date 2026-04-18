import type { ElementDefinition } from "cytoscape";
import type { GraphEdge, GraphNode, GraphResponse } from "../types/api";

export type ElementData = {
  elements: ElementDefinition[];
  yearRange: [number, number];
};

export type ElementOptions = {
  includeSimilarity?: boolean;
};

/**
 * Converts the backend graph payload into Cytoscape element definitions.
 * Each node carries `size` (citation-scaled) and `color` (year-gradient) as
 * data() properties so the stylesheet can map them with mapData().
 */
export function toElements(
  response: GraphResponse,
  opts: ElementOptions = {},
): ElementData {
  const years = response.nodes
    .map((n) => n.year ?? 0)
    .filter((y) => y > 0);
  const minYear = years.length ? Math.min(...years) : 2000;
  const maxYear = years.length ? Math.max(...years) : 2020;

  const elements: ElementDefinition[] = [];

  for (const n of response.nodes) {
    elements.push({
      group: "nodes",
      data: {
        id: n.id,
        label: truncateLabel(n.title),
        title: n.title,
        year: n.year ?? 0,
        citationCount: n.citationCount ?? 0,
        authors: n.authors ?? [],
        isSeed: n.isSeed === true,
        similarity: n.similarity,
        size: nodeSize(n),
        color: yearColor(n.year, minYear, maxYear),
      },
      classes: n.isSeed ? "seed" : "",
    });
  }

  const includeSimilarity = opts.includeSimilarity ?? true;
  for (const e of response.edges) {
    if (e.kind === "similarity" && !includeSimilarity) continue;
    elements.push({
      group: "edges",
      data: {
        id: edgeId(e),
        source: e.source,
        target: e.target,
        kind: e.kind,
        weight: e.weight ?? 1,
      },
      classes: e.kind,
    });
  }

  return { elements, yearRange: [minYear, maxYear] };
}

function truncateLabel(title: string): string {
  if (title.length <= 60) return title;
  return title.slice(0, 57) + "…";
}

export function nodeSize(n: GraphNode): number {
  const cc = n.citationCount ?? 0;
  const base = 18 + Math.log10(cc + 1) * 10;
  const clamped = Math.max(18, Math.min(80, base));
  return n.isSeed ? clamped + 8 : clamped;
}

/**
 * Maps a publication year onto a blue→red gradient via HSL.
 * Older papers are cool-toned (blue), newer papers warm-toned (red).
 */
export function yearColor(year: number | undefined, minYear: number, maxYear: number): string {
  if (!year || year <= 0) return "#9ca3af";
  if (maxYear === minYear) return "hsl(220, 70%, 55%)";
  const t = Math.min(1, Math.max(0, (year - minYear) / (maxYear - minYear)));
  // hue 220 (blue) -> 12 (red-orange)
  const hue = 220 - t * 208;
  return `hsl(${hue.toFixed(0)}, 70%, 55%)`;
}

function edgeId(e: GraphEdge): string {
  return `${e.kind}:${e.source}->${e.target}`;
}
