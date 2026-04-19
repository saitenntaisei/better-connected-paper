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
  const simWeights = response.edges
    .filter((e) => e.kind === "similarity")
    .map((e) => e.weight ?? 0);
  const simMin = simWeights.length ? Math.min(...simWeights) : 0;
  const simMax = simWeights.length ? Math.max(...simWeights) : 1;

  for (const e of response.edges) {
    if (e.kind === "similarity" && !includeSimilarity) continue;
    const weight = e.weight ?? 1;
    const data: Record<string, unknown> = {
      id: edgeId(e),
      source: e.source,
      target: e.target,
      kind: e.kind,
      weight,
    };
    if (e.kind === "similarity") {
      const style = similarityEdgeStyle(weight, simMin, simMax);
      data.color = style.color;
      data.opacity = style.opacity;
      data.lineWidth = style.width;
    }
    elements.push({ group: "edges", data, classes: e.kind });
  }

  return { elements, yearRange: [minYear, maxYear] };
}

function truncateLabel(title: string): string {
  if (title.length <= 60) return title;
  return title.slice(0, 57) + "…";
}

const CITATION_CAP = 10000;

export function nodeSize(n: GraphNode): number {
  const cc = Math.min(n.citationCount ?? 0, CITATION_CAP);
  const base = 6 + Math.log2(cc + 1) * 5.5;
  const clamped = Math.max(10, Math.min(80, base));
  return n.isSeed ? clamped + 6 : clamped;
}

export function similarityEdgeStyle(
  weight: number,
  min: number,
  max: number,
): { color: string; opacity: number; width: number } {
  const range = max - min;
  const raw = range > 0 ? (weight - min) / range : 0.5;
  const t = Math.min(1, Math.max(0, raw));
  const lightness = 78 - t * 55;
  const opacity = 0.3 + t * 0.6;
  const width = 1 + t * 3;
  return {
    color: `hsl(215, 18%, ${lightness.toFixed(0)}%)`,
    opacity,
    width,
  };
}

/**
 * Maps a publication year onto a grayscale gradient.
 * Older papers are lighter gray, newer papers are darker gray.
 */
export function yearColor(year: number | undefined, minYear: number, maxYear: number): string {
  if (!year || year <= 0) return "#9ca3af";
  if (maxYear === minYear) return "hsl(0, 0%, 65%)";
  const t = Math.min(1, Math.max(0, (year - minYear) / (maxYear - minYear)));
  // lightness 82% (old, light gray) -> 38% (new, dark gray)
  const lightness = 82 - t * 44;
  return `hsl(0, 0%, ${lightness.toFixed(0)}%)`;
}

function edgeId(e: GraphEdge): string {
  return `${e.kind}:${e.source}->${e.target}`;
}
