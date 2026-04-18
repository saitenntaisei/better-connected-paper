// Mirrors the JSON shapes served by backend/internal/api/*.go.
// Keep in sync with the Go types; any divergence will surface in client tests.

export type HealthResponse = {
  status: string;
  time: string;
};

export type SearchResult = {
  id: string;
  title: string;
  year?: number;
  authors?: string[];
  venue?: string;
  citationCount?: number;
  abstract?: string;
};

export type SearchResponse = {
  total: number;
  results: SearchResult[];
};

export type Paper = {
  paperId: string;
  title: string;
  abstract?: string;
  year?: number;
  venue?: string;
  authors?: Array<{ authorId?: string; name: string }>;
  citationCount?: number;
  referenceCount?: number;
  influentialCitationCount?: number;
  externalIds?: Record<string, string>;
  url?: string;
};

export type GraphNode = {
  id: string;
  title: string;
  abstract?: string;
  year?: number;
  venue?: string;
  authors?: string[];
  citationCount?: number;
  referenceCount?: number;
  externalIds?: Record<string, string>;
  url?: string;
  similarity: number;
  isSeed?: boolean;
};

export type EdgeKind = "cite" | "similarity";

export type GraphEdge = {
  source: string;
  target: string;
  kind: EdgeKind;
  weight?: number;
};

export type GraphResponse = {
  seed: GraphNode;
  nodes: GraphNode[];
  edges: GraphEdge[];
  builtAt: string;
};

export type ApiError = {
  error: string;
};
