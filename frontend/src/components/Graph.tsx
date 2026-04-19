import { useEffect, useRef } from "react";
import cytoscape from "cytoscape";
import type { Core } from "cytoscape";
import coseBilkent from "cytoscape-cose-bilkent";
import { toElements } from "../lib/graphElements";
import { graphStylesheet } from "../lib/cytoscapeStyles";
import type { GraphResponse } from "../types/api";

let registered = false;
function ensureRegistered() {
  if (registered) return;
  cytoscape.use(coseBilkent);
  registered = true;
}

type Props = {
  data: GraphResponse;
  onSelectNode?: (id: string) => void;
  showSimilarity?: boolean;
};

export function Graph({ data, onSelectNode, showSimilarity = true }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);

  useEffect(() => {
    ensureRegistered();
    if (!containerRef.current) return;

    const { elements } = toElements(data, { includeSimilarity: showSimilarity });
    const cy = cytoscape({
      container: containerRef.current,
      elements,
      style: graphStylesheet,
      layout: {
        name: "cose-bilkent",
        animate: false,
        idealEdgeLength: 220,
        nodeRepulsion: 22000,
        tile: true,
        fit: true,
        padding: 40,
      } as cytoscape.LayoutOptions,
      wheelSensitivity: 0.3,
    });
    cyRef.current = cy;

    const tapHandler = (evt: cytoscape.EventObject) => {
      onSelectNode?.(evt.target.id());
    };
    cy.on("tap", "node", tapHandler);

    const clearHighlight = () => {
      cy.elements().removeClass("path-dim path-hit");
    };
    const hoverHandler = (evt: cytoscape.EventObject) => {
      const start: cytoscape.NodeSingular = evt.target;
      const reachable = cy.collection();
      reachable.merge(start);
      const seen = new Set<string>([start.id()]);
      const queue: cytoscape.NodeSingular[] = [start];
      while (queue.length > 0) {
        const n = queue.shift()!;
        const outEdges = n.outgoers("edge.cite").edges();
        reachable.merge(outEdges);
        outEdges.forEach((edge) => {
          const t = edge.target();
          if (!seen.has(t.id())) {
            seen.add(t.id());
            reachable.merge(t);
            queue.push(t);
          }
        });
      }
      const simEdges = start.connectedEdges("edge.similarity");
      reachable.merge(simEdges);
      simEdges.forEach((edge) => {
        reachable.merge(edge.source());
        reachable.merge(edge.target());
      });
      cy.elements().addClass("path-dim");
      reachable.removeClass("path-dim").addClass("path-hit");
    };
    cy.on("mouseover", "node", hoverHandler);
    cy.on("mouseout", "node", clearHighlight);

    return () => {
      cy.off("tap", "node", tapHandler);
      cy.off("mouseover", "node", hoverHandler);
      cy.off("mouseout", "node", clearHighlight);
      cy.destroy();
      cyRef.current = null;
    };
  }, [data, onSelectNode, showSimilarity]);

  return (
    <div
      ref={containerRef}
      className="graph-container"
      role="img"
      aria-label={`Citation graph for ${data.seed.title}`}
      data-testid="graph"
    />
  );
}
