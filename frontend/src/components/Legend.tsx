type Props = {
  yearRange?: [number, number];
};

export function Legend({ yearRange }: Props) {
  const [minYear, maxYear] = yearRange ?? [2000, 2020];
  return (
    <aside className="legend" aria-label="Graph legend">
      <h3 className="legend-title">Legend</h3>
      <div className="legend-row">
        <span className="swatch seed" aria-hidden="true" />
        <span>Seed paper</span>
      </div>
      <div className="legend-row">
        <span className="swatch cite" aria-hidden="true">→</span>
        <span>A → B: A cites B</span>
      </div>
      <div className="legend-row">
        <span className="swatch similarity" aria-hidden="true">⋯</span>
        <span>Similarity link (dashed)</span>
      </div>
      <div className="legend-gradient">
        <span className="muted">{minYear}</span>
        <span className="gradient" aria-hidden="true" />
        <span className="muted">{maxYear}</span>
      </div>
      <p className="legend-hint muted">
        Node size scales with citation count.
      </p>
    </aside>
  );
}
