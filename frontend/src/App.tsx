import { useCallback, useEffect, useMemo, useState } from "react";
import { SearchBar } from "./components/SearchBar";
import { ResultsList } from "./components/ResultsList";
import { Graph } from "./components/Graph";
import { PaperDetail } from "./components/PaperDetail";
import { Legend } from "./components/Legend";
import { Spinner } from "./components/Spinner";
import { useSearch } from "./hooks/useSearch";
import { useGraph } from "./hooks/useGraph";
import { useUrlSeed } from "./hooks/useUrlSeed";
import type { SearchResult } from "./types/api";

export default function App() {
  const { state: searchState, runSearch } = useSearch({ limit: 10 });
  const { state: graphState, build } = useGraph();
  const { seed: urlSeed, setSeed: setUrlSeed } = useUrlSeed();

  const [seedId, setSeedId] = useState<string | null>(urlSeed);
  const [focusId, setFocusId] = useState<string | null>(urlSeed);
  const [showSimilarity, setShowSimilarity] = useState(false);

  useEffect(() => {
    if (!seedId) return;
    void build(seedId);
  }, [seedId, build]);

  const selectSeed = useCallback(
    (result: SearchResult) => {
      setSeedId(result.id);
      setFocusId(result.id);
      setUrlSeed(result.id);
    },
    [setUrlSeed],
  );

  const loading = searchState.status === "loading";
  const results = useMemo(
    () => (searchState.status === "success" ? searchState.data.results : []),
    [searchState],
  );
  const seedTitle = useMemo(() => {
    if (graphState.status === "success") return graphState.data.seed.title;
    const hit = results.find((r) => r.id === seedId);
    return hit?.title ?? seedId;
  }, [graphState, results, seedId]);
  const yearRange = useMemo<[number, number] | undefined>(() => {
    if (graphState.status !== "success") return undefined;
    const years = graphState.data.nodes.map((n) => n.year ?? 0).filter((y) => y > 0);
    if (years.length === 0) return undefined;
    return [Math.min(...years), Math.max(...years)];
  }, [graphState]);

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
            {loading ? <Spinner label="Searching" /> : null}
          </h2>
          <ResultsList
            results={results}
            selectedId={seedId ?? undefined}
            onSelect={selectSeed}
          />
        </section>
      )}

      {seedId ? (
        <section className="graph-section" aria-labelledby="graph-heading">
          <div className="graph-header">
            <h2 id="graph-heading" className="section-heading">
              Citation graph
              <span className="muted"> — seed: {seedTitle}</span>
            </h2>
            <label className="toggle">
              <input
                type="checkbox"
                checked={showSimilarity}
                onChange={(e) => setShowSimilarity(e.target.checked)}
              />
              Show similarity links
            </label>
          </div>
          <div className="graph-layout">
            <div className="graph-main">
              {graphState.status === "loading" && <Spinner label="Building graph" />}
              {graphState.status === "error" && (
                <div className="error-box" role="alert">
                  <p>{graphState.error}</p>
                  <button
                    type="button"
                    onClick={() => void build(seedId, true)}
                    className="retry"
                  >
                    Retry
                  </button>
                </div>
              )}
              {graphState.status === "success" && (
                <Graph
                  data={graphState.data}
                  onSelectNode={setFocusId}
                  showSimilarity={showSimilarity}
                />
              )}
            </div>
            <div className="graph-side">
              <Legend yearRange={yearRange} />
              <PaperDetail id={focusId} />
            </div>
          </div>
        </section>
      ) : null}
    </main>
  );
}
