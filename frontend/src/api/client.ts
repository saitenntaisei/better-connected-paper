import type {
  GraphResponse,
  HealthResponse,
  Paper,
  SearchResponse,
} from "../types/api";

const DEFAULT_BASE =
  (import.meta.env.VITE_API_BASE as string | undefined)?.replace(/\/$/, "") || "/api";

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

export type ClientOptions = {
  baseUrl?: string;
  fetch?: typeof fetch;
  signal?: AbortSignal;
};

async function request<T>(
  path: string,
  init: RequestInit,
  opts: ClientOptions = {},
): Promise<T> {
  const base = opts.baseUrl ?? DEFAULT_BASE;
  const doFetch = opts.fetch ?? fetch;
  const res = await doFetch(base + path, {
    ...init,
    signal: opts.signal ?? init.signal,
    headers: {
      Accept: "application/json",
      ...init.headers,
    },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      // non-JSON error body — fall through with statusText
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export function getHealth(opts?: ClientOptions): Promise<HealthResponse> {
  return request<HealthResponse>("/health", { method: "GET" }, opts);
}

export function search(
  query: string,
  limit?: number,
  opts?: ClientOptions,
): Promise<SearchResponse> {
  const params = new URLSearchParams({ q: query });
  if (limit) params.set("limit", String(limit));
  return request<SearchResponse>(
    `/search?${params.toString()}`,
    { method: "GET" },
    opts,
  );
}

export function getPaper(id: string, opts?: ClientOptions): Promise<Paper> {
  return request<Paper>(`/paper/${encodeURIComponent(id)}`, { method: "GET" }, opts);
}

export function buildGraph(
  seedId: string,
  fresh = false,
  opts?: ClientOptions,
): Promise<GraphResponse> {
  return request<GraphResponse>(
    "/graph/build",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ seedId, fresh }),
    },
    opts,
  );
}

export function getCachedGraph(
  seedId: string,
  opts?: ClientOptions,
): Promise<GraphResponse> {
  return request<GraphResponse>(
    `/graph/${encodeURIComponent(seedId)}`,
    { method: "GET" },
    opts,
  );
}
