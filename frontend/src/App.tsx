import { useState } from "react";
import { SearchBar } from "./components/SearchBar";
import { ResultsList } from "./components/ResultsList";
import { useSearch } from "./hooks/useSearch";
import type { SearchResult } from "./types/api";

export default function App() {
  const { state, runSearch } = useSearch({ limit: 10 });
  const [selected, setSelected] = useState<SearchResult | null>(null);

  const loading = state.status === "loading";
  const results = state.status === "success" ? state.data.results : [];

  return (
    <main className="app-shell">
      <header>
        <h1>Better Connected Paper</h1>
        <p className="tagline">
          Citation-aware paper explorer — the directed graph Connected Papers doesn&apos;t show.
        </p>
      </header>

      <SearchBar onSubmit={runSearch} busy={loading} />

      {state.status === "error" && (
        <p className="error" role="alert">
          {state.error}
        </p>
      )}

      {state.status !== "idle" && (
        <section aria-labelledby="results-heading">
          <h2 id="results-heading" className="section-heading">
            Results
            {state.status === "success" ? (
              <span className="muted"> ({state.data.total.toLocaleString()} found)</span>
            ) : null}
          </h2>
          <ResultsList
            results={results}
            selectedId={selected?.id}
            onSelect={setSelected}
          />
        </section>
      )}

      {selected ? (
        <aside className="selection" aria-live="polite">
          <h2 className="section-heading">Selected seed</h2>
          <p className="selection-title">{selected.title}</p>
          {selected.abstract ? (
            <p className="selection-abstract">{selected.abstract}</p>
          ) : null}
          <p className="muted">Graph build wiring lands in the next commit.</p>
        </aside>
      ) : null}
    </main>
  );
}
