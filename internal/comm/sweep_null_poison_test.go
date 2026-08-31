package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// *** A FILE CAN NOW BE OFFERED TO A ROOM, WHICH IS THE POINT OF MIGRATION 0017. ***
//
// This test asserted the OPPOSITE one release ago: that the refusal at least said "files are
// channel-only today" instead of pointing at a `to_room` parameter that did not exist. That
// was the honest thing to say while it was true. It is not true any more, so the test says
// what replaced it rather than being deleted — the limitation and its removal are the same
// story and this is where it is told.
func TestAFileCanBeOfferedToARoom(t *testing.T) {
	ctx := context.Background()
	lim := DefaultLimits()
	lim.FilesEnabled = true
	st := newStore(t, lim)
	a := stationEndpoint(t, st, "tok-a", "st-alpha")
	stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "r-ops", "s:st-alpha", "s:st-beta")

	res, err := st.OfferFile(ctx, a, FileAddr{RoomID: "r-ops"}, FileOffer{
		Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"})
	if err != nil {
		t.Fatalf("offering a file to a room: %v", err)
	}
	if res.Attachment.ScopeID != "r:r-ops" {
		t.Fatalf("attachment scope = %q, want r:r-ops", res.Attachment.ScopeID)
	}

	// A NON-MEMBER IS REFUSED. Membership is the authorisation for a room offer exactly as
	// it is for a room send — one rule, not a file-specific second one.
	outsider := stationEndpoint(t, st, "tok-c", "st-gamma")
	if _, err := st.OfferFile(ctx, outsider, FileAddr{RoomID: "r-ops"}, FileOffer{
		Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"}); err == nil {
		t.Fatal("a non-member offered a file into a room")
	}
}

// AND TO A LINKED STATION, which is the hole the original finding understated: comm_send
// {to_station} is the path the instructions steer every session toward, and it could not
// carry a file at all because a pair deliberately has no channel row.
func TestAFileCanBeOfferedToALinkedStation(t *testing.T) {
	ctx := context.Background()
	lim := DefaultLimits()
	lim.FilesEnabled = true
	st := newStore(t, lim)
	a := stationEndpoint(t, st, "tok-a", "st-alpha")
	stationEndpoint(t, st, "tok-b", "st-beta")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	res, err := st.OfferFile(ctx, a, FileAddr{StationID: "st-beta"}, FileOffer{
		Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"})
	if err != nil {
		t.Fatalf("offering a file to a linked station: %v", err)
	}
	if res.Attachment.ScopeID != "p:st-alpha|st-beta" {
		t.Fatalf("attachment scope = %q, want the sorted pair scope", res.Attachment.ScopeID)
	}
	// THE SAME SCOPE THE CONVERSATION USES. If these diverged the file would land somewhere
	// comm_poll{scope:"p:..."} never looks, which is the failure the pair model exists to avoid.
	m, err := st.SendToStation(ctx, a, "st-beta", "and here is the file", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Scope != res.Attachment.ScopeID {
		t.Fatalf("the message went to %q and the file to %q — the conversation is split in two",
			m.Scope, res.Attachment.ScopeID)
	}

	// UNLINKED IS REFUSED, by the same check a pair send uses.
	stationEndpoint(t, st, "tok-c", "st-gamma")
	if _, err := st.OfferFile(ctx, a, FileAddr{StationID: "st-gamma"}, FileOffer{
		Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"}); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("offering to an unlinked station: got %v, want ErrNotLinked", err)
	}
}

// EXACTLY ONE ADDRESS. Zero is a caller who does not know how to address it; two is a caller
// who thinks a file goes to two places, and which one it went to would decide who can fetch it.
func TestOfferFileRequiresExactlyOneAddress(t *testing.T) {
	ctx := context.Background()
	lim := DefaultLimits()
	lim.FilesEnabled = true
	st := newStore(t, lim)
	a := stationEndpoint(t, st, "tok-a", "st-alpha")
	o := FileOffer{Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"}

	for _, c := range []struct {
		name string
		addr FileAddr
	}{
		{"none", FileAddr{}},
		{"room and station", FileAddr{RoomID: "r-ops", StationID: "st-beta"}},
	} {
		_, err := st.OfferFile(ctx, a, c.addr, o)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%s: %v — the refusal should name the three and say exactly one", c.name, err)
		}
	}
}
