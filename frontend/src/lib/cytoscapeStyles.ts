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
      width: "data(lineWidth)",
      "line-color": "data(color)",
      "line-style": "dashed",
      "curve-style": "bezier",
      opacity: "data(opacity)",
    },
  },
  {
    selector: ".path-dim",
    css: {
      opacity: 0.15,
      "text-opacity": 0.15,
    },
  },
  {
    selector: "node.path-hit",
    css: {
      opacity: 1,
      "text-opacity": 1,
      "border-width": 3,
      "border-color": "#111827",
      "border-opacity": 1,
    },
  },
  {
    selector: "node.seed.path-hit",
    css: {
      "border-width": 4,
      "border-color": "#f59e0b",
      "border-opacity": 1,
    },
  },
  {
    selector: "edge.path-hit",
    css: {
      opacity: 1,
      width: 3,
      "line-color": "#111827",
      "target-arrow-color": "#111827",
    },
  },
];
