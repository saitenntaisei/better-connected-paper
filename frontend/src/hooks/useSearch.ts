import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, search as searchApi } from "../api/client";
import type { SearchResponse } from "../types/api";

export type SearchState =
  | { status: "idle" }
  | { status: "loading"; query: string }
  | { status: "success"; query: string; data: SearchResponse }
  | { status: "error"; query: string; error: string };

type Options = {
  limit?: number;
};

export function useSearch({ limit = 10 }: Options = {}) {
  const [state, setState] = useState<SearchState>({ status: "idle" });
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  const runSearch = useCallback(
    async (query: string) => {
      const trimmed = query.trim();
      if (!trimmed) return;
      abortRef.current?.abort();
      const ctl = new AbortController();
      abortRef.current = ctl;
      setState({ status: "loading", query: trimmed });
      try {
        const data = await searchApi(trimmed, limit, { signal: ctl.signal });
        if (ctl.signal.aborted) return;
        setState({ status: "success", query: trimmed, data });
      } catch (err) {
        if (ctl.signal.aborted) return;
        const message =
          err instanceof ApiError
            ? err.message
            : err instanceof Error
              ? err.message
              : "search failed";
        setState({ status: "error", query: trimmed, error: message });
      }
    },
    [limit],
  );

  const reset = useCallback(() => {
    abortRef.current?.abort();
    setState({ status: "idle" });
  }, []);

  return { state, runSearch, reset };
}
