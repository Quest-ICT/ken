package commserver

import (
	"context"
	"testing"
)

// PROBE (temporary): is the comm/KB wall enforced at USE time, or only at mint time?
func TestProbeMixedScopeTokenOnCommEndpoint(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	mixed := mintToken(t, st, "mixed-agent", "read", "write-draft", "propose", "comm")

	p, err := authenticate(ctx, st, mixed, ScopeComm)
	if err != nil {
		t.Fatalf("WALL ENFORCED AT USE: mixed token refused on comm: %v", err)
	}
	t.Logf("WALL NOT ENFORCED AT USE: mixed token accepted on /comm/mcp; actor=%d scopes=%v", p.ActorID, p.Scopes)
}
