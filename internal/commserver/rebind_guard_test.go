package commserver

import (
	"os"
	"strings"
	"testing"
)

// *** THE REFUSAL MUST NOT CLAIM AN INVARIANT THE PRODUCT DOES NOT HOLD. ***
//
// comm_bind refuses a bound endpoint, and for four releases the refusal said "an endpoint cannot
// move between stations, because it would carry the first station's unread mail into the second."
// Both halves were false, in opposite directions:
//
//   - NOT PREVENTED. comm_unbind clears station_id, and its own success note says "You can bind
//     again later" — so unbind-then-bind performs the move, and the tool doing the bypass
//     advertises it. One boolean on a column another tool clears is not an invariant.
//   - THE HARM NAMED CANNOT HAPPEN. delivery.party_key is stamped at write time and no delivery
//     row is ever moved, so the old station's unread mail stays filed under the old party.
//
// A false invariant is worse than an honest guard, because it is exactly what persuades the next
// reader that the unbind-then-bind route is safe. This pins the honest version: the refusal names
// the real consequence (channel seats follow the live binding), does not deny the mail claim by
// silence, and does not assert prevention it cannot deliver.
func TestTheRebindRefusalDoesNotClaimAnInvariant(t *testing.T) {
	msg := bindRefusalText(t)

	for _, gone := range []string{
		"an endpoint cannot move between stations",
		"carry the first station's unread mail into the second",
	} {
		if strings.Contains(msg, gone) {
			t.Errorf("the bind refusal still claims %q — it is not enforced, and the harm it names "+
				"cannot happen; a reader who tests the claim finds comm_unbind and concludes the "+
				"route is blessed", gone)
		}
	}
	for _, c := range []struct {
		property string
		anyOf    []string
	}{
		{"that channel seats follow the live binding — the real consequence",
			[]string{"seat", "seats"}},
		{"that the old station's mail does NOT travel, so nobody re-derives the retired fear",
			[]string{"does NOT come with you", "stays filed"}},
		{"what to do instead", []string{"Register a new endpoint"}},
	} {
		found := false
		for _, w := range c.anyOf {
			if strings.Contains(msg, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the bind refusal does not say %s", c.property)
		}
	}

	// AND THE UNBIND NOTE MUST NOT CONTRADICT IT. "You can bind again later" standing alone is
	// what made the two tools disagree in the first place — one forbidding what the other offers.
	if !strings.Contains(unbindNoteText(t), "different one") {
		t.Error("comm_unbind's success note still offers rebinding with no qualification, which is " +
			"the half of the contradiction that reads as permission")
	}
}

func bindRefusalText(t *testing.T) string {
	t.Helper()
	return sourceSlice(t, "this endpoint is already bound to a station")
}

func unbindNoteText(t *testing.T) string {
	t.Helper()
	return sourceSlice(t, "other readers stays with them")
}

// sourceSlice returns the shipped string literal containing marker, reassembled from the source
// so the test reads what a caller receives rather than a copy that can drift from it.
func sourceSlice(t *testing.T, marker string) string {
	t.Helper()
	b, err := readFileString("commserver.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(b, marker)
	if i < 0 {
		t.Fatalf("no shipped string contains %q", marker)
	}
	start := strings.LastIndex(b[:i], "\n")
	end := strings.Index(b[i:], "\n\t\t}")
	if end < 0 {
		end = 400
	}
	return b[start : i+end]
}

func readFileString(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
