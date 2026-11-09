package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// defaultHealthURL is the hub's health endpoint on its default address:
// 200 once its database answers with every migration applied. It needs no
// sign-in and answers on any Host, also with org subdomains on.
const defaultHealthURL = "http://127.0.0.1:7788/healthz"

// healthcheck is `dash healthcheck [url]`: exit 0 if Dash answers url with
// 200 within a few seconds, else 1. It is the Docker image's HEALTHCHECK,
// which has no shell or curl to do it with.
func healthcheck(args []string) int {
	u := defaultHealthURL
	if len(args) > 0 {
		u = args[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := checkHealth(ctx, u); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	return 0
}

func checkHealth(ctx context.Context, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", u, resp.Status)
	}
	return nil
}
