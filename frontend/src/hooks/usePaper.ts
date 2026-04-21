import { useEffect, useRef, useState } from "react";
import { ApiError, getPaper } from "../api/client";
import type { Paper } from "../types/api";

export type PaperState =
  | { status: "idle" }
  | { status: "loading"; id: string }
  | { status: "success"; id: string; data: Paper }
  | { status: "error"; id: string; error: string };

export function usePaper(id: string | null | undefined) {
  const [state, setState] = useState<PaperState>(
    id ? { status: "loading", id } : { status: "idle" },
  );
  const [prevId, setPrevId] = useState(id);
  const abortRef = useRef<AbortController | null>(null);

  if (id !== prevId) {
    setPrevId(id);
    setState(id ? { status: "loading", id } : { status: "idle" });
  }

  useEffect(() => {
    abortRef.current?.abort();
    if (!id) return;
    const ctl = new AbortController();
    abortRef.current = ctl;
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
