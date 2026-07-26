package store

import (
	"context"
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
