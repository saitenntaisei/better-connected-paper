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

  useEffect(() => () => abortRef.current?.abort(), []);

  const build = useCallback(async (seedId: string, fresh = false) => {
    const trimmed = seedId.trim();
    if (!trimmed) return;
    abortRef.current?.abort();
    const ctl = new AbortController();
    abortRef.current = ctl;
    setState({ status: "loading", seedId: trimmed });
    try {
      const data = await buildGraph(trimmed, fresh, { signal: ctl.signal });
      if (ctl.signal.aborted) return;
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
