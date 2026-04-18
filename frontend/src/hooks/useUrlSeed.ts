import { useCallback, useEffect, useState } from "react";

const PARAM = "seed";

function readFromLocation(): string | null {
  if (typeof window === "undefined") return null;
  const url = new URL(window.location.href);
  return url.searchParams.get(PARAM);
}

/**
 * Syncs a paper ID to the `?seed=` query string.
 * - Initial state reads from the URL so the graph rehydrates on refresh.
 * - setSeed() replaces history state (no new entry) to keep the back button useful.
 * - popstate listens to user navigation so back/forward reloads the right graph.
 */
export function useUrlSeed() {
  const [seed, setSeedState] = useState<string | null>(() => readFromLocation());

  useEffect(() => {
    const handler = () => setSeedState(readFromLocation());
    window.addEventListener("popstate", handler);
    return () => window.removeEventListener("popstate", handler);
  }, []);

  const setSeed = useCallback((id: string | null) => {
    const url = new URL(window.location.href);
    if (id) url.searchParams.set(PARAM, id);
    else url.searchParams.delete(PARAM);
    window.history.replaceState({}, "", url.toString());
    setSeedState(id);
  }, []);

  return { seed, setSeed };
}
