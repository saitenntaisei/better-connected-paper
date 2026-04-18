package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type healthResponse struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

func Health(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		Time:   time.Now().UTC(),
	})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
