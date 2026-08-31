package stationserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestTheDirectoryRowCarriesTheAddress fails if station_directory lists a station without the one
// field needed to reach it.
//
// THE TOOL EXISTED TO ANSWER "who is here and how do I write to them" AND ANSWERED HALF.
// comm_send{to_station} takes a station_id and nothing else produces one: the pairing code is
// deleted, links are created by the first message, and comm_directory is on a surface a caller may
// not be looking at. So a row with a name, a purpose, last_seen_at, linked and staffed — and no id
// — describes a station the caller cannot address. Found in the first five minutes of using the
// 4.0.0 surface: the only way to reach ken-prod was a human pasting its id.
//
// It is asserted over the SERIALISED row built by newDirEntry, not a struct literal: the two
// fields at issue are strings, so a literal that omits them marshals cleanly and lists a station
// nobody can reach. Testing the constructor is testing the only path the handler uses.
func TestTheDirectoryRowCarriesTheAddress(t *testing.T) {
	b, err := json.Marshal(newDirEntry("abc123", "peer"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got, _ := row["station_id"].(string); got != "abc123" {
		t.Errorf("a directory row serialises no usable station_id (got %q) — every field it does "+
			"carry is descriptive, so the listing names stations it gives no way to reach:\n%s",
			got, b)
	}

	// The hint must name the PARAMETER, not merely exist: to_station is one of three addressing
	// arguments on comm_send, and an id with no parameter name still leaves the caller guessing.
	hint, _ := row["address_with"].(string)
	if !strings.Contains(hint, "to_station") {
		t.Errorf("address_with does not name the parameter the id belongs in: %q", hint)
	}
}

// TestBothDirectoriesAgreeOnHowToAddressAStation pins the two surfaces together.
//
// The station row's own comment claimed it MIRRORED comm_directory's while omitting both fields
// that make a row actionable — a comment that documented the intent and the drift at once. Nothing
// compared them, so nothing said otherwise.
func TestBothDirectoriesAgreeOnHowToAddressAStation(t *testing.T) {
	need := []string{"station_id", "address_with"}
	ty := reflect.TypeOf(dirEntry{})
	have := map[string]bool{}
	for i := 0; i < ty.NumField(); i++ {
		name := strings.Split(ty.Field(i).Tag.Get("json"), ",")[0]
		have[name] = true
	}
	for _, f := range need {
		if !have[f] {
			t.Errorf("station_directory's row has no %q, which comm_directory's row carries. "+
				"Two directories over one estate must not disagree about how to address it.", f)
		}
	}
}
