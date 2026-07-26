package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseStructured(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.md", `---
name: node-require-esm-only-package
description: "Requiring an ESM-only package from CommonJS throws ERR_REQUIRE_ESM."
metadata:
  type: project
---

require() of an ESM-only package throws ERR_REQUIRE_ESM.

**Why:** the two module systems load differently — ESM resolution is asynchronous.

**How to apply:** use a dynamic import() from CommonJS. See [[node-esm]] and [[bundler-interop]] and [[node-esm]].
`)
	m, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Slug != "node-require-esm-only-package" || m.Kind != "project" {
		t.Fatalf("slug/kind: %q %q", m.Slug, m.Kind)
	}
	if !strings.HasPrefix(m.Summary, "Requiring an ESM-only") {
		t.Fatalf("summary: %q", m.Summary)
	}
	if !strings.Contains(m.Problem, "ERR_REQUIRE_ESM") {
		t.Fatalf("problem: %q", m.Problem)
	}
	if !strings.Contains(m.Rationale, "asynchronous") {
		t.Fatalf("rationale: %q", m.Rationale)
	}
	if !strings.Contains(m.Solution, "dynamic import()") {
		t.Fatalf("solution: %q", m.Solution)
	}
	if len(m.Links) != 2 { // deduped
		t.Fatalf("links: %+v", m.Links)
	}
}

func TestParseUnstructured(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "u.md", `---
name: user-email
description: The user's email
metadata:
  type: user
---

The user's email address is alice@example.com.
`)
	m, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "user" {
		t.Fatalf("kind: %q", m.Kind)
	}
	if m.Problem != "" || !strings.Contains(m.Solution, "alice@example.com") {
		t.Fatalf("unstructured body should go to Solution: problem=%q solution=%q", m.Problem, m.Solution)
	}
}

func TestParseUnknownTypeAndNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "k.md", "---\nname: n\nmetadata:\n  type: wat\n---\nbody\n")
	m, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "reference" { // unknown type falls back
		t.Fatalf("kind: %q", m.Kind)
	}
	bad := write(t, dir, "bad.md", "no frontmatter here\n")
	if _, err := ParseFile(bad); err == nil {
		t.Fatal("expected error for a file without frontmatter")
	}
}
