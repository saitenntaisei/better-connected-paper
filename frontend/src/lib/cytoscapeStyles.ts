import type { StylesheetCSS } from "cytoscape";

export const graphStylesheet: StylesheetCSS[] = [
  {
    selector: "node",
    css: {
      width: "data(size)",
      height: "data(size)",
      "background-color": "data(color)",
      label: "data(label)",
      color: "#1a1410",
      "font-family": "Bricolage Grotesque, Helvetica Neue, sans-serif",
      "font-size": 10,
      "font-weight": 500,
      "text-valign": "bottom",
      "text-halign": "center",
      "text-margin-y": 5,
      "text-wrap": "wrap",
      "text-max-width": "130px",
      "border-width": 1,
      "border-color": "#1a1410",
      "border-opacity": 0.35,
    },
  },
  {
    selector: "node.seed",
    css: {
      "border-width": 4,
      "border-color": "#b4432c",
      "border-opacity": 1,
      "font-weight": 600,
      color: "#7a2817",
    },
  },
  {
    selector: "edge.cite",
    css: {
      width: 1.4,
      "line-color": "#7a6f62",
      "target-arrow-color": "#7a6f62",
      "target-arrow-shape": "triangle",
      "curve-style": "straight",
      "arrow-scale": 1.1,
      opacity: 0.8,
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
      "border-color": "#1a1410",
      "border-opacity": 1,
    },
  },
  {
    selector: "node.seed.path-hit",
    css: {
      "border-width": 4,
      "border-color": "#b4432c",
      "border-opacity": 1,
    },
  },
  {
    selector: "edge.path-hit",
    css: {
      opacity: 1,
      width: 3,
      "line-color": "#b4432c",
      "target-arrow-color": "#b4432c",
    },
  },
];
