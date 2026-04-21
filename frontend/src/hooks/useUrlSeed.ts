import { useCallback, useEffect, useState } from "react";

const PARAM = "seed";
const STATE_MARK = "bcp-seed";

function readFromLocation(): string | null {
  if (typeof window === "undefined") return null;
  const url = new URL(window.location.href);
  return url.searchParams.get(PARAM);
}

/**
 * Syncs a paper ID to the `?seed=` query string.
 *
 * Transitions between the search view (no seed) and the graph view (with seed)
 * push a new history entry so browser Back navigates between them. Other
 * updates replace the current entry to avoid flooding history.
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
    const previous = readFromLocation();
    if (id) url.searchParams.set(PARAM, id);
    else url.searchParams.delete(PARAM);
    // Push whenever we land on a new non-null seed so browser Back returns to
    // the previous view — covers both null → seed (search selection) and
    // seed → seed (drill-in via node double-click). seed → null and no-op
    // writes use replaceState to avoid polluting history.
    if (id && previous !== id) {
      window.history.pushState({ kind: STATE_MARK }, "", url.toString());
    } else {
      window.history.replaceState(window.history.state, "", url.toString());
    }
    setSeedState(id);
  }, []);

  const hasPushedEntry = useCallback(() => {
    if (typeof window === "undefined") return false;
    return window.history.state?.kind === STATE_MARK;
  }, []);

  return { seed, setSeed, hasPushedEntry };
}
