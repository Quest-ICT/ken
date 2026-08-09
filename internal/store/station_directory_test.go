package store

import (
	"context"
	"strings"
	"testing"
)

// The directory's visibility rule, exercised on every branch at once.
//
// This test exists as much for the SQL as for the rule: the query refers to a SELECT
// alias inside its WHERE clause, which SQLite accepts as an extension and standard
// SQL does not. A Go build cannot tell a valid query from an invalid one — the query
// is a string — so the only thing that establishes it works is running it.
func TestListStationsVisibleToAppliesTheVisibilityRule(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	actor, err := s.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}

	mk := func(name string, published bool) *Station {
		t.Helper()
		st, err := s.CreateStation(ctx, 1, name, "", actor)
		if err != nil {
			t.Fatal(err)
		}
		if published {
			if err := s.SetStationPublished(ctx, st.StationID, true); err != nil {
				t.Fatal(err)
			}
		}
		return st
	}

	me := mk("me", true)
	pub := mk("published-stranger", true)
	linked := mk("unpublished-peer", false)
	hidden := mk("unpublished-stranger", false)
	arch := mk("archived-but-published", true)

	// An active link between me and the unpublished peer.
	reqID, err := s.CreateStationLinkRequest(ctx, 1, "tok", me.StationID, linked.StationID, "work", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveLinkRequest(ctx, reqID, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveStation(ctx, arch.StationID, true); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListStationsVisibleTo(ctx, 1, me.StationID)
	if err != nil {
		t.Fatalf("the directory query failed to run: %v", err)
	}
	seen := map[string]DirectoryEntry{}
	for _, e := range got {
		seen[e.Name] = e
	}

	// PUBLISHED is visible, and this is the first reader `published` has ever had.
	if _, ok := seen["published-stranger"]; !ok {
		t.Error("a published station is not listed — `published` still gates nothing")
	}
	// LINKED is visible even though unpublished: hiding an existing relationship from
	// the party that holds it would make the directory disagree with what
	// comm_open_channel will actually do.
	if e, ok := seen["unpublished-peer"]; !ok {
		t.Error("an unpublished station I hold an active link to is not listed")
	} else if !e.Linked {
		t.Error("the peer I hold a link to is listed with Linked=false")
	}
	// The published stranger is visible but NOT addressable, and the two facts must
	// not be collapsed.
	if e := seen["published-stranger"]; e.Linked {
		t.Error("a published station I have no link to reports Linked=true — discovery and permission are being conflated")
	}
	// NEGATIVE CASES. Each of these passing for the wrong reason would make the test
	// vacuous, which is why the positives above are asserted in the same run.
	if _, ok := seen["unpublished-stranger"]; ok {
		t.Error("an unpublished station I have no link to is listed — the directory leaks every station's existence")
	}
	if _, ok := seen["archived-but-published"]; ok {
		t.Error("an archived station is listed as available for comm")
	}
	if _, ok := seen["me"]; ok {
		t.Error("the asking station lists itself")
	}
	if len(got) != 2 {
		t.Fatalf("directory returned %d entries, want exactly 2: %+v", len(got), got)
	}

	// Revoking the link must remove the unpublished peer, or visibility outlives the
	// permission that granted it.
	links, err := s.ListStationLinks(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeStationLink(ctx, links[0].LinkID); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListStationsVisibleTo(ctx, 1, me.StationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if e.Name == "unpublished-peer" {
			t.Fatal("an unpublished station stayed visible after its link was revoked")
		}
	}
	_ = pub
	_ = hidden
}

// The one agent-writable station field must be BOUNDED, because slice 2 made it flow
// verbatim into every peer's context through the directory tools.
//
// It was uncapped. A 700 KiB self-description was accepted and returned byte-identical
// to a different station — an unbounded write into other agents' working memory, from
// the only field an agent controls. Every sibling payload is capped (notebook pages
// 64 KiB, locker blobs 256 KiB, task text 512 B); this one was reachable only by the
// MCP body limit.
func TestSelfDescriptionIsCappedBecauseItReachesOtherAgents(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	actor, err := s.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.CreateStation(ctx, 1, "verbose", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL: an ordinary self-description is accepted, so a later refusal cannot be
	// blamed on the writer being broken.
	if err := s.SetStationSelfDescription(ctx, st.StationID, "I run the backups.", []string{"ops"}); err != nil {
		t.Fatalf("an ordinary self-description was refused: %v", err)
	}

	huge := strings.Repeat("x", MaxSelfDescriptionBytes+1)
	if err := s.SetStationSelfDescription(ctx, st.StationID, huge, nil); err == nil {
		t.Fatalf("a %d-byte self-description was accepted — it reaches every peer that lists the directory", len(huge))
	}
	// REFUSED, not truncated: a shortened description is a claim the station never made.
	got, err := s.StationByID(ctx, st.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SelfDescribedAbout != "I run the backups." {
		t.Fatalf("the refused write altered the stored value: %q", got.SelfDescribedAbout)
	}

	// Tags are bounded separately — the byte cap alone permits thousands of tiny tags.
	many := make([]string, MaxSelfDescriptionTags+1)
	for i := range many {
		many[i] = "t"
	}
	if err := s.SetStationSelfDescription(ctx, st.StationID, "ok", many); err == nil {
		t.Fatalf("%d tags were accepted", len(many))
	}
	if err := s.SetStationSelfDescription(ctx, st.StationID, "ok", []string{strings.Repeat("y", 65)}); err == nil {
		t.Fatal("a 65-byte tag was accepted")
	}
}
