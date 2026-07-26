// Package version carries the build version of Ken and the location of its
// Corresponding Source (the AGPL-3.0 §13 network-interaction obligation).
package version

import (
	"os"
	"runtime"
	"strings"
)

// DevVersion is the value compiled into a source build — i.e. any binary whose
// version was NOT injected by the release script. Used to tell an operator that
// what they are running is not a published release artifact.
const DevVersion = "1.1.0-dev"

// Version is the current Ken version. Overridable at build time via -ldflags.
var Version = DevVersion

// defaultSourceURL is where Ken's own source lives. It is the fallback only:
// SourceURL() prefers the operator's KEN_SOURCE_URL.
const defaultSourceURL = "https://github.com/Quest-ICT/ken"

// sourceURL is the build-time source location, overridable via -ldflags
// (build-release.sh sets it from $KEN_SOURCE_URL). Prefer SourceURL().
var sourceURL = defaultSourceURL

// SourceURL returns where THIS running instance's Corresponding Source can be
// obtained. It is displayed in the web UI (including the unauthenticated login
// and setup pages) to satisfy AGPL-3.0 §13, which requires a network-interactive
// modified version to offer ITS OWN source to remote users.
//
// Resolution order: the KEN_SOURCE_URL environment variable, then the build-time
// value, then this project's repository. The environment override is what makes
// the obligation dischargeable by a FORK: anyone running modified Ken can point
// the link at their own repository without patching the binary. Without it, a
// modified instance would advertise upstream's repository — source that does not
// correspond to the running program — putting the operator in violation.
func SourceURL() string {
	if u := strings.TrimSpace(os.Getenv("KEN_SOURCE_URL")); u != "" {
		return u
	}
	return sourceURL
}

// IsReleaseBuild reports whether the version was injected at build time, i.e.
// this binary came from the release pipeline rather than a plain `go build`.
func IsReleaseBuild() bool { return Version != DevVersion }

// Line is the one-line human-readable build identity printed by `ken version`.
// A source build is marked explicitly: the version it reports is the compiled-in
// placeholder, not a released artifact, and saying so prevents a bug report
// against a version that was never published. It also surfaces a silently failed
// -ldflags injection (an unknown -X symbol is NOT an error for `go build`).
func Line() string {
	s := "Ken " + Version + " " + runtime.GOOS + "/" + runtime.GOARCH
	if !IsReleaseBuild() {
		s += " (source build — not a release artifact)"
	}
	return s
}
