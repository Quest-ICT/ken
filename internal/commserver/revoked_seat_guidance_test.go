package commserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// *** THE REFUSAL FOR A DEAD SEAT MUST CROSS THE MAPPER, AND MUTATION PROVED IT WOULD NOT. ***
//
// The gate in comm.ChannelFor refuses a send whose peer seat is revoked and unbound — mail filed
// under `e:<rowid>` for a rowid nothing can ever hold again. It wraps ErrChannelClosed and marks
// itself CallerSafe.
//
// A test at the raise site passed against a mutant with CallerSafe REMOVED, because at the store
// layer err.Error() returns the full text either way. commError flattens by sentinel, so without
// the marker the caller would read the generic "channel is not open — both sessions must join the
// pairing code, and it must not be revoked" — advice to re-join a channel that is open, for a
// failure that has nothing to do with joining.
//
// That is the 3.3.0 defect verbatim: a correct string in the binary, unreachable from every
// caller, with a test one layer below it passing. So this one crosses the boundary — the error
// comes from the REAL gate and goes through the REAL mapper.
func TestTheDeadSeatRefusalSurvivesTheErrorMapper(t *testing.T) {
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	a, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok-a", ActorID: 1}, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok-b", ActorID: 1}, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := cs.MintPairingCode(ctx, 1, "dev<->peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := cs.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL: while both seats live, the channel resolves. Without this the refusal below could
	// be any breakage rather than the one under test.
	if _, _, err := cs.ChannelFor(ctx, a, ch.ChannelID); err != nil {
		t.Fatalf("a healthy channel did not resolve: %v", err)
	}

	if err := cs.RevokeEndpoint(ctx, b.EndpointID); err != nil {
		t.Fatal(err)
	}
	_, _, err = cs.ChannelFor(ctx, a, ch.ChannelID)
	if err == nil {
		t.Fatal("the gate accepted a channel whose peer seat can never be held again")
	}

	got := commError(err).Error()
	if !strings.Contains(got, "revoked") {
		t.Fatalf("the caller reads %q.\n"+
			"The refusal names revocation at the raise site and the mapper discarded it — so a "+
			"session is told to re-join a pairing code for a channel that is open, which is the "+
			"one thing that cannot help.", got)
	}
	if !strings.Contains(got, "to_station") {
		t.Errorf("the caller is not told what to do instead: %q", got)
	}
	// AND IT MUST NOT BE THE GENERIC ONE. Asserting only the presence of a word would pass if the
	// mapper concatenated both.
	if strings.Contains(got, "both sessions must join the pairing code") {
		t.Errorf("the caller got the generic channel-closed advice: %q", got)
	}
}
