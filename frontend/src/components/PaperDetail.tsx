import { usePaper } from "../hooks/usePaper";
import type { Paper } from "../types/api";

type Props = {
  id: string | null;
};

export function PaperDetail({ id }: Props) {
  const state = usePaper(id);

  if (!id || state.status === "idle") {
    return (
      <aside className="detail placeholder" aria-live="polite">
        <p className="muted">Click a node to see paper details.</p>
      </aside>
    );
  }
  if (state.status === "loading") {
    return (
      <aside className="detail" aria-live="polite">
        <p className="muted" role="status">
          Loading paper…
        </p>
      </aside>
    );
  }
  if (state.status === "error") {
    return (
      <aside className="detail" aria-live="polite">
        <p className="error" role="alert">
          {state.error}
        </p>
      </aside>
    );
  }

  return <PaperCard paper={state.data} />;
}

function PaperCard({ paper }: { paper: Paper }) {
  return (
    <aside className="detail" aria-live="polite">
      <h3 className="detail-title">{paper.title}</h3>
      <p className="detail-meta">
        {paper.year ? <span>{paper.year}</span> : null}
        {paper.venue ? <span>{paper.venue}</span> : null}
        {typeof paper.citationCount === "number" ? (
          <span>{paper.citationCount.toLocaleString()} citations</span>
        ) : null}
        {typeof paper.referenceCount === "number" ? (
          <span>{paper.referenceCount.toLocaleString()} refs</span>
        ) : null}
      </p>
      {paper.authors && paper.authors.length > 0 ? (
        <p className="detail-authors">
          {paper.authors.map((a) => a.name).join(", ")}
        </p>
      ) : null}
      {paper.abstract ? (
        <p className="detail-abstract">{paper.abstract}</p>
      ) : (
        <p className="muted">No abstract available.</p>
      )}
      <ExternalLinks paper={paper} />
    </aside>
  );
}

function ExternalLinks({ paper }: { paper: Paper }) {
  const doi = paper.externalIds?.DOI;
  const arxiv = paper.externalIds?.ArXiv;
  const links: { href: string; label: string }[] = [];
  if (paper.url) links.push({ href: paper.url, label: "Semantic Scholar" });
  if (doi) links.push({ href: `https://doi.org/${doi}`, label: "DOI" });
  if (arxiv) links.push({ href: `https://arxiv.org/abs/${arxiv}`, label: "arXiv" });
  if (links.length === 0) return null;
  return (
    <ul className="detail-links">
      {links.map((l) => (
        <li key={l.label}>
          <a href={l.href} target="_blank" rel="noreferrer noopener">
            {l.label}
          </a>
        </li>
      ))}
    </ul>
  );
}
