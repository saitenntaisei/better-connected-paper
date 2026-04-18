import { useEffect, useState } from "react";
import { SearchBar } from "./components/SearchBar";
import { ResultsList } from "./components/ResultsList";
import { Graph } from "./components/Graph";
import { useSearch } from "./hooks/useSearch";
import { useGraph } from "./hooks/useGraph";
import type { SearchResult } from "./types/api";

export default function App() {
  const { state: searchState, runSearch } = useSearch({ limit: 10 });
  const { state: graphState, build } = useGraph();
  const [selected, setSelected] = useState<SearchResult | null>(null);

  useEffect(() => {
    if (selected) void build(selected.id);
  }, [selected, build]);

  const loading = searchState.status === "loading";
  const results = searchState.status === "success" ? searchState.data.results : [];

  return (
    <main className="app-shell">
      <header>
        <h1>Better Connected Paper</h1>
        <p className="tagline">
          Citation-aware paper explorer — the directed graph Connected Papers doesn&apos;t show.
        </p>
      </header>

      <SearchBar onSubmit={runSearch} busy={loading} />

      {searchState.status === "error" && (
        <p className="error" role="alert">
          {searchState.error}
        </p>
      )}

      {searchState.status !== "idle" && (
        <section aria-labelledby="results-heading">
          <h2 id="results-heading" className="section-heading">
            Results
            {searchState.status === "success" ? (
              <span className="muted"> ({searchState.data.total.toLocaleString()} found)</span>
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
        <section className="graph-section" aria-labelledby="graph-heading">
          <h2 id="graph-heading" className="section-heading">
            Citation graph
            <span className="muted"> — seed: {selected.title}</span>
          </h2>
          {graphState.status === "loading" && (
            <p className="muted" role="status">Building graph…</p>
          )}
          {graphState.status === "error" && (
            <p className="error" role="alert">{graphState.error}</p>
          )}
          {graphState.status === "success" && <Graph data={graphState.data} />}
        </section>
      ) : null}
    </main>
  );
}
