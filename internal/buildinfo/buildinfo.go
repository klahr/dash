// Package buildinfo holds the version a release build is stamped with.
package buildinfo

// Version is the release, e.g. "1.2.0", set at link time by GoReleaser:
//
//	go build -ldflags "-X github.com/klahr/dash/internal/buildinfo.Version=1.2.0" ./cmd/dash
//
// A build from source without that flag reports "dev".
var Version = "dev"
