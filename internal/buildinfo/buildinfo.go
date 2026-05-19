// Package buildinfo holds build-time-injected metadata.
package buildinfo

// Version is the semver-ish version, overridable at link time:
//
//	go build -ldflags "-X github.com/bcrisp4/bns/internal/buildinfo.Version=v0.1.0"
var Version = "dev"
