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
  const { seed: urlSeed, setSeed: setUrlSeed, hasPushedEntry } = useUrlSeed();

  const [focusId, setFocusId] = useState<string | null>(urlSeed);
  const [showSimilarity, setShowSimilarity] = useState(true);

  // Reset the detail-panel focus whenever the seed changes — including the
  // popstate case, which updates urlSeed without going through selectSeed.
  useEffect(() => {
    setFocusId(urlSeed);
  }, [urlSeed]);

  useEffect(() => {
    if (!urlSeed) return;
    void build(urlSeed);
  }, [urlSeed, build]);

  const selectSeed = useCallback(
    (result: SearchResult) => {
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
    const hit = results.find((r) => r.id === urlSeed);
    return hit?.title ?? urlSeed;
  }, [graphState, results, urlSeed]);
  const yearRange = useMemo<[number, number] | undefined>(() => {
    if (graphState.status !== "success") return undefined;
    const years = graphState.data.nodes.map((n) => n.year ?? 0).filter((y) => y > 0);
    if (years.length === 0) return undefined;
    return [Math.min(...years), Math.max(...years)];
  }, [graphState]);

  if (urlSeed) {
    return (
      <main className="graph-page" aria-labelledby="graph-heading">
        <div className="graph-page-header">
          <button
            type="button"
            className="graph-back"
            onClick={() => {
              if (hasPushedEntry()) window.history.back();
              else setUrlSeed(null);
            }}
            aria-label="Back to search"
          >
            ← Back
          </button>
          <h2 id="graph-heading" className="graph-page-title">
            Citation graph
            <span className="muted"> — {seedTitle}</span>
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
        <div className="graph-page-body">
          <div className="graph-main">
            {graphState.status === "loading" && <Spinner label="Building graph" />}
            {graphState.status === "error" && (
              <div className="error-box" role="alert">
                <p>{graphState.error}</p>
                <button
                  type="button"
                  onClick={() => void build(urlSeed, true)}
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
          <aside className="graph-side">
            <Legend yearRange={yearRange} />
            <PaperDetail id={focusId} />
          </aside>
        </div>
      </main>
    );
  }

  return (
    <main className="app-shell">
      <header>
        <h1>Better Connected Paper</h1>
        <p className="tagline">
          Citation-aware paper explorer — the directed graph Connected Papers doesn&apos;t show.
        </p>
      </header>

      <SearchBar
        onSubmit={runSearch}
        busy={loading}
        initial={searchState.status === "idle" ? "" : searchState.query}
      />

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
            selectedId={urlSeed ?? undefined}
            onSelect={selectSeed}
          />
        </section>
      )}
    </main>
  );
}
