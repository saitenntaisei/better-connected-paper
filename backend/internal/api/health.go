package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type healthResponse struct {
	Status      string            `json:"status"`
	Time        time.Time         `json:"time"`
	Persistence persistenceStatus `json:"persistence"`
}

// persistenceStatus surfaces whether caching-to-Postgres is actually wired up.
// "enabled" reflects configuration intent (DATABASE_URL was set); "healthy"
// reflects current reachability. A false/true split lets ops tell "cache
// disabled by config" apart from "cache is supposed to work but is down."
type persistenceStatus struct {
	Enabled bool   `json:"enabled"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
}

// Health returns a lightweight readiness probe. Returns 200 even when the
// DB is unreachable so Vercel keeps routing traffic to the function (S2
// reads still work without the cache); operators consult the persistence
// block to decide if the cache is degraded.
func (d Deps) Health(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:      "ok",
		Time:        time.Now().UTC(),
		Persistence: d.checkPersistence(r.Context()),
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (d Deps) checkPersistence(ctx context.Context) persistenceStatus {
	if d.DB == nil || d.DB.Pool == nil {
		return persistenceStatus{Enabled: false, Healthy: false}
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := d.DB.Pool.Ping(pingCtx); err != nil {
		return persistenceStatus{Enabled: true, Healthy: false, Error: err.Error()}
	}
	return persistenceStatus{Enabled: true, Healthy: true}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
