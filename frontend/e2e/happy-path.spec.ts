import { test, expect, type Route } from "@playwright/test";

// Minimal backend fixtures, just enough to exercise the happy-path flow.
const searchBody = {
  total: 1,
  results: [
    {
      id: "seed-1",
      title: "Attention Is All You Need",
      year: 2017,
      authors: ["Ashish Vaswani", "Noam Shazeer"],
      citationCount: 12345,
      venue: "NeurIPS",
    },
  ],
};

const graphBody = {
  seed: {
    id: "seed-1",
    title: "Attention Is All You Need",
    year: 2017,
    citationCount: 12345,
  },
  nodes: [
    {
      id: "seed-1",
      title: "Attention Is All You Need",
      year: 2017,
      citationCount: 12345,
      similarity: 1,
      isSeed: true,
    },
    {
      id: "neighbor-1",
      title: "BERT: Pre-training of Deep Bidirectional Transformers",
      year: 2018,
      citationCount: 9876,
      similarity: 0.8,
      isSeed: false,
    },
  ],
  edges: [{ source: "neighbor-1", target: "seed-1", kind: "cite" }],
  builtAt: "2026-04-18T12:00:00Z",
};

const paperBody = {
  id: "seed-1",
  title: "Attention Is All You Need",
  year: 2017,
  venue: "NeurIPS",
  citationCount: 12345,
  referenceCount: 40,
  authors: [{ name: "Ashish Vaswani" }, { name: "Noam Shazeer" }],
  abstract:
    "The dominant sequence transduction models are based on complex recurrent or convolutional neural networks…",
  externalIds: { DOI: "10.48550/arXiv.1706.03762", ArXiv: "1706.03762" },
  url: "https://www.semanticscholar.org/paper/seed-1",
};

function json(route: Route, body: unknown) {
  return route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

test("search → build graph → inspect paper detail", async ({ page }) => {
  await page.route("**/api/search**", (route) => json(route, searchBody));
  await page.route("**/api/graph/build", (route) => json(route, graphBody));
  await page.route("**/api/paper/seed-1", (route) => json(route, paperBody));

  await page.goto("/");

  await expect(
    page.getByRole("heading", { name: "Better Connected Paper" }),
  ).toBeVisible();

  await page.getByRole("searchbox", { name: /paper/i }).fill("attention");
  await page.getByRole("button", { name: /search/i }).click();

  const resultButton = page.getByRole("option", {
    name: /Attention Is All You Need/,
  });
  await expect(resultButton).toBeVisible();
  await resultButton.click();

  await expect(
    page.getByRole("heading", { name: /Citation graph/i }),
  ).toBeVisible();
  await expect(page.locator(".detail-title")).toContainText(
    "Attention Is All You Need",
  );
  await expect(page.locator(".detail-abstract")).toContainText(
    "dominant sequence transduction",
  );
  await expect(page.getByRole("link", { name: "Semantic Scholar" })).toHaveAttribute(
    "href",
    "https://www.semanticscholar.org/paper/seed-1",
  );
  await expect(page).toHaveURL(/\?seed=seed-1$/);
});

test("reload with ?seed= rehydrates the graph", async ({ page }) => {
  await page.route("**/api/graph/build", (route) => json(route, graphBody));
  await page.route("**/api/paper/seed-1", (route) => json(route, paperBody));

  await page.goto("/?seed=seed-1");

  await expect(
    page.getByRole("heading", { name: /Citation graph/i }),
  ).toBeVisible();
  await expect(page.locator(".detail-title")).toContainText(
    "Attention Is All You Need",
  );
});
