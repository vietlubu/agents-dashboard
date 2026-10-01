// Package version carries the build version, injected at build time with
// -ldflags "-X github.com/vietlubu/agent-dashboard/internal/version.Version=x.y.z".
package version

// Version is the application version. The default marks an unstamped development build.
var Version = "dev"
