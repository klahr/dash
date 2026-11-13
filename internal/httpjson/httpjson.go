// Package httpjson writes JSON responses: the one shape every Dash HTTP
// handler answers in.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// Write answers with code and v encoded as JSON.
func Write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Error answers with code and {"error": msg}.
func Error(w http.ResponseWriter, code int, msg string) {
	Write(w, code, map[string]string{"error": msg})
}
