import type { StylesheetCSS } from "cytoscape";

export const graphStylesheet: StylesheetCSS[] = [
  {
    selector: "node",
    css: {
      width: "data(size)",
      height: "data(size)",
      "background-color": "data(color)",
      label: "data(label)",
      color: "#111827",
      "font-size": 10,
      "text-valign": "bottom",
      "text-halign": "center",
      "text-margin-y": 4,
      "text-wrap": "wrap",
      "text-max-width": "120px",
      "border-width": 1,
      "border-color": "#1f2937",
      "border-opacity": 0.4,
    },
  },
  {
    selector: "node.seed",
    css: {
      "border-width": 4,
      "border-color": "#f59e0b",
      "border-opacity": 1,
      "font-weight": "bold",
    },
  },
  {
    selector: "edge.cite",
    css: {
      width: 1.5,
      "line-color": "#6b7280",
      "target-arrow-color": "#6b7280",
      "target-arrow-shape": "triangle",
      "curve-style": "straight",
      "arrow-scale": 1.2,
      opacity: 0.85,
    },
  },
  {
    selector: "edge.similarity",
    css: {
      width: "mapData(weight, 0, 1, 1, 4)",
      "line-color": "#9ca3af",
      "line-style": "dashed",
      "curve-style": "bezier",
      opacity: 0.5,
    },
  },
];
