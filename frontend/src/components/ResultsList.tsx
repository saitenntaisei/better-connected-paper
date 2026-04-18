import type { SearchResult } from "../types/api";

type Props = {
  results: SearchResult[];
  selectedId?: string;
  onSelect: (result: SearchResult) => void;
};

export function ResultsList({ results, selectedId, onSelect }: Props) {
  if (results.length === 0) {
    return (
      <p className="results-empty" role="status">
        No results yet. Search for a paper above.
      </p>
    );
  }
  return (
    <ul className="results-list" role="listbox" aria-label="Search results">
      {results.map((r) => {
        const selected = r.id === selectedId;
        return (
          <li key={r.id}>
            <button
              type="button"
              className={selected ? "result selected" : "result"}
              role="option"
              aria-selected={selected}
              onClick={() => onSelect(r)}
            >
              <span className="result-title">{r.title || "(untitled)"}</span>
              <span className="result-meta">
                {r.year ? <span>{r.year}</span> : null}
                {r.venue ? <span>{r.venue}</span> : null}
                {typeof r.citationCount === "number" ? (
                  <span>{r.citationCount.toLocaleString()} citations</span>
                ) : null}
              </span>
              {r.authors && r.authors.length > 0 ? (
                <span className="result-authors">{r.authors.slice(0, 4).join(", ")}</span>
              ) : null}
            </button>
          </li>
        );
      })}
    </ul>
  );
}
