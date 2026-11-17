package api

import (
	"net/http"
	"testing"
)

// Only a runner's plain "not found" (the answer for a project id it doesn't
// know) makes the hub link the project again; other answers don't.
func TestRunnerLostProject(t *testing.T) {
	for _, c := range []struct {
		code int
		body string
		want bool
	}{
		{http.StatusNotFound, `{"error":"not found"}`, true},
		{http.StatusNotFound, `{"error":"no such commit"}`, false},
		{http.StatusNotFound, `not json`, false},
		{http.StatusOK, `{"error":"not found"}`, false},
	} {
		if got := runnerLostProject(forwarded{code: c.code, body: []byte(c.body)}); got != c.want {
			t.Errorf("%d %s: %v, want %v", c.code, c.body, got, c.want)
		}
	}
}
