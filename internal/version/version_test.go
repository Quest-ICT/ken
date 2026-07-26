package version

import (
	"strings"
	"testing"
)

// TestSourceURLEnvOverride is the AGPL §13 guarantee for FORKS: an operator running
// a modified Ken must be able to point the in-app "Source" link at their own
// repository. Without this, a modified instance advertises upstream's source — which
// does not correspond to the running program — putting that operator in violation.
func TestSourceURLEnvOverride(t *testing.T) {
	if got := SourceURL(); got != sourceURL {
		t.Fatalf("with no env set, SourceURL() = %q, want the build-time value %q", got, sourceURL)
	}
	t.Setenv("KEN_SOURCE_URL", "https://example.org/my-fork")
	if got := SourceURL(); got != "https://example.org/my-fork" {
		t.Fatalf("SourceURL() = %q, want the env override", got)
	}
	// Whitespace-only must NOT blank the link (an empty footer href is worse than
	// upstream's: it discharges nothing and looks broken).
	t.Setenv("KEN_SOURCE_URL", "   ")
	if got := SourceURL(); got != sourceURL {
		t.Fatalf("blank env should fall back to %q, got %q", sourceURL, got)
	}
}

// TestLineMarksSourceBuild: `ken version` must distinguish a plain `go build` from a
// release artifact. This is also the ONLY runtime surface that reveals a silently
// failed -ldflags injection — `go build` does not error on an unknown -X symbol, so a
// stale symbol path would otherwise ship a binary quietly reporting the dev version.
func TestLineMarksSourceBuild(t *testing.T) {
	if Version != DevVersion {
		t.Skipf("test binary was built with an injected version (%s)", Version)
	}
	line := Line()
	if !strings.Contains(line, "source build") {
		t.Fatalf("Line() = %q, want it to mark an uninjected build", line)
	}
	if IsReleaseBuild() {
		t.Fatal("IsReleaseBuild() must be false when Version is the compiled-in default")
	}
}
