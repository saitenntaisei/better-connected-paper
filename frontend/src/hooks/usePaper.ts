import { useEffect, useRef, useState } from "react";
import { ApiError, getPaper } from "../api/client";
import type { Paper } from "../types/api";

export type PaperState =
  | { status: "idle" }
  | { status: "loading"; id: string }
  | { status: "success"; id: string; data: Paper }
  | { status: "error"; id: string; error: string };

export function usePaper(id: string | null | undefined) {
  const [state, setState] = useState<PaperState>({ status: "idle" });
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    abortRef.current?.abort();
    if (!id) {
      setState({ status: "idle" });
      return;
    }
    const ctl = new AbortController();
    abortRef.current = ctl;
    setState({ status: "loading", id });
    getPaper(id, { signal: ctl.signal })
      .then((data) => {
        if (ctl.signal.aborted) return;
        setState({ status: "success", id, data });
      })
      .catch((err) => {
        if (ctl.signal.aborted) return;
        const message =
          err instanceof ApiError
            ? err.message
            : err instanceof Error
              ? err.message
              : "failed to load paper";
        setState({ status: "error", id, error: message });
      });
    return () => ctl.abort();
  }, [id]);

  return state;
}
