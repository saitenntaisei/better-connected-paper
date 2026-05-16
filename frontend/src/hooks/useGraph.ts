import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, buildGraph } from "../api/client";
import type { GraphResponse } from "../types/api";

export type GraphState =
  | { status: "idle" }
  | { status: "loading"; seedId: string }
  | { status: "success"; seedId: string; data: GraphResponse }
  | { status: "error"; seedId: string; error: string };

export function useGraph() {
  const [state, setState] = useState<GraphState>({ status: "idle" });
  const abortRef = useRef<AbortController | null>(null);
  // Per-session graph cache. Two-level so the same paper has a single
  // payload no matter which alias the user reaches it through:
  //   - byCanonical: canonical seed id → GraphResponse
  //   - aliasToCanonical: any lookup key (raw user input, DOI, canonical
  //     id) → canonical seed id
  // A fresh rebuild from any alias overwrites the canonical entry, so
  // every prior alias starts seeing the refreshed graph. Without this
  // indirection, Retry from the canonical URL would leave the DOI
  // alias serving the stale snapshot on Back/Forward.
  const cacheRef = useRef<{
    byCanonical: Map<string, GraphResponse>;
    aliasToCanonical: Map<string, string>;
  }>({ byCanonical: new Map(), aliasToCanonical: new Map() });

  useEffect(() => () => abortRef.current?.abort(), []);

  const build = useCallback(async (seedId: string, fresh = false) => {
    const trimmed = seedId.trim();
    if (!trimmed) return;
    abortRef.current?.abort();
    if (!fresh) {
      const canonical = cacheRef.current.aliasToCanonical.get(trimmed);
      const cached = canonical
        ? cacheRef.current.byCanonical.get(canonical)
        : undefined;
      if (cached) {
        setState({ status: "success", seedId: trimmed, data: cached });
        return;
      }
    }
    const ctl = new AbortController();
    abortRef.current = ctl;
    setState({ status: "loading", seedId: trimmed });
    try {
      const data = await buildGraph(trimmed, fresh, { signal: ctl.signal });
      if (ctl.signal.aborted) return;
      const canonicalId = data.seed.id || trimmed;
      cacheRef.current.byCanonical.set(canonicalId, data);
      cacheRef.current.aliasToCanonical.set(trimmed, canonicalId);
      cacheRef.current.aliasToCanonical.set(canonicalId, canonicalId);
      setState({ status: "success", seedId: trimmed, data });
    } catch (err) {
      if (ctl.signal.aborted) return;
      const message =
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : "graph build failed";
      setState({ status: "error", seedId: trimmed, error: message });
    }
  }, []);

  const reset = useCallback(() => {
    abortRef.current?.abort();
    setState({ status: "idle" });
  }, []);

  return { state, build, reset };
}
