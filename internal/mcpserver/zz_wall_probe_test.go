package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// PROBE (temporary): the same mixed-scope token on the knowledge-base endpoint.
func TestProbeMixedScopeTokenOnKBEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "ken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	id, err := st.FindOrCreateActor(ctx, "ai", "mixed-agent")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.IssueToken(ctx, id, []string{"read", "write-draft", "propose", "comm"}, "probe")
	if err != nil {
		t.Fatal(err)
	}

	p, err := authenticate(ctx, st, tok)
	if err != nil {
		t.Fatalf("mixed token refused on /mcp: %v", err)
	}
	c := withPrincipal(ctx, p)
	for _, sc := range []string{scopeRead, scopeWriteDraft, scopePropose, scopeCurate} {
		t.Logf("scope %-12s -> %v", sc, requireScope(c, sc))
	}
	t.Logf("mixed token accepted on /mcp; scopes=%v", p.Scopes)
}
