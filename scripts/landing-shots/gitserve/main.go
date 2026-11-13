// Command gitserve serves the bare repositories in a folder over HTTPS with
// git http-backend and a throwaway certificate, as the end-to-end tests do,
// so a demo runner can clone and push a project whose remote is a reserved
// example.com address (mapped here with git's url.<base>.insteadOf).
//
//	go run ./scripts/landing-shots/gitserve -root /tmp/landing-demo/origin \
//		-backend "$(git --exec-path)/git-http-backend"
package main

import (
	"flag"
	"log"
	"net"
	"net/http/cgi"
	"net/http/httptest"
)

func main() {
	root := flag.String("root", "", "folder with the bare repositories")
	addr := flag.String("addr", "127.0.0.1:7792", "listen address")
	backend := flag.String("backend", "", "path of git-http-backend: $(git --exec-path)/git-http-backend")
	flag.Parse()
	if *root == "" || *backend == "" {
		log.Fatal("-root and -backend are required")
	}
	l, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	s := httptest.NewUnstartedServer(&cgi.Handler{Path: *backend, Env: []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1"}})
	s.Listener = l
	s.StartTLS()
	log.Printf("serving %s at %s", *root, s.URL)
	select {}
}
