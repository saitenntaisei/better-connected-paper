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
};

export function Graph({ data, onSelectNode }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);

  useEffect(() => {
    ensureRegistered();
    if (!containerRef.current) return;

    const { elements } = toElements(data);
    const cy = cytoscape({
      container: containerRef.current,
      elements,
      style: graphStylesheet,
      layout: {
        name: "cose-bilkent",
        animate: false,
        idealEdgeLength: 120,
        nodeRepulsion: 9000,
        tile: true,
        fit: true,
        padding: 30,
      } as cytoscape.LayoutOptions,
      wheelSensitivity: 0.3,
    });
    cyRef.current = cy;

    const handler = (evt: cytoscape.EventObject) => {
      onSelectNode?.(evt.target.id());
    };
    cy.on("tap", "node", handler);

    return () => {
      cy.off("tap", "node", handler);
      cy.destroy();
      cyRef.current = null;
    };
  }, [data, onSelectNode]);

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
