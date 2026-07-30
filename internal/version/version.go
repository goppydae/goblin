// Package version holds the build-time version identity for goblin binaries,
// mirroring gapi's core/version pattern. The root VERSION file is the single
// source of version truth; magelib.Version() resolves it at build time.
package version

// Version is injected at build time via
// -ldflags "-X github.com/goppydae/goblin/internal/version.Version=<v>".
  var Version = "dev"
