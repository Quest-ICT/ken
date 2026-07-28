package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeleteExpiredSessions(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	aid, err := st.FindOrCreateActor(ctx, "human", "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(ctx, aid, -time.Hour); err != nil { // already expired
		t.Fatal(err)
	}
	live, err := st.CreateSession(ctx, aid, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	n, err := st.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected to purge exactly 1 expired session, got %d", n)
	}
	if _, err := st.SessionByID(ctx, live.ID); err != nil {
		t.Fatalf("the live session should remain: %v", err)
	}
}

// The stored session id must be a HASH, never the cookie value: the database is copied
// by design (snapshots), and a raw session id in a copy is a replayable login for
// whoever reads it. This is the same property api_token has always had.
func TestSessionCookieIsNeverStoredRaw(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	actorID, err := st.CreateHumanUser(ctx, "curator", "x")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(ctx, actorID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// The raw cookie must appear NOWHERE in the row that was written.
	var stored string
	if err := st.R.QueryRowContext(ctx, `SELECT id FROM web_session`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == sess.ID {
		t.Fatal("the cookie value is stored raw — a snapshot would carry a replayable login")
	}
	if want := sessionKey(sess.ID); stored != want {
		t.Fatalf("stored id = %q, want the sha256 hex %q", stored, want)
	}

	// And the round trip still works: the raw cookie resolves, the hash does not.
	got, err := st.SessionByID(ctx, sess.ID)
	if err != nil {
		t.Fatalf("raw cookie should resolve: %v", err)
	}
	if got.ActorID != actorID || got.ID != sess.ID {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, err := st.SessionByID(ctx, stored); !errors.Is(err, ErrNotFound) {
		t.Fatal("the STORED hash must not be accepted as a cookie — that would defeat hashing")
	}

	// Logout still works when given the raw value.
	if err := st.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByID(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("session should be gone after logout")
	}
}
