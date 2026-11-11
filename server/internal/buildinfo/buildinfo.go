// Package buildinfo identifies the running build.
package buildinfo

// Version is set at link time:
//
//	-ldflags "-X github.com/hmnd/secure-immich-share/server/internal/buildinfo.Version=v1.2.3"
var Version = "dev"

const RepoURL = "https://github.com/hmnd/secure-immich-share"
