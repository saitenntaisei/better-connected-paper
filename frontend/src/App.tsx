import { useEffect, useState } from "react";
import { getHealth } from "./api/client";

export default function App() {
  const [status, setStatus] = useState<"loading" | "ok" | "error">("loading");

  useEffect(() => {
    getHealth()
      .then((h) => setStatus(h.status === "ok" ? "ok" : "error"))
      .catch(() => setStatus("error"));
  }, []);

  return (
    <main className="app-shell">
      <header>
        <h1>Better Connected Paper</h1>
        <p className="tagline">
          Citation-aware paper explorer — the directed graph Connected Papers doesn&apos;t show.
        </p>
      </header>
      <section className="status" data-testid="backend-status">
        Backend: <strong>{status}</strong>
      </section>
    </main>
  );
}
