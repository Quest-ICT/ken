package comm

import (
	"context"
)

// Staffing and the two lookups below have nothing to do with channels. They lived in channel.go
// only because that is where the endpoint queries happened to be written, and they outlived the
// channel by design: a station's mailbox, who is reading it, and how much is waiting are the facts
// the directory and the console are built on.
type Staffing struct {
	Endpoints  int    // live (non-revoked) endpoints bound to the station
	LastSeenAt string // freshest last_seen_at across them; empty when Endpoints is 0
}

// StaffingByStation reports staffing for every station COMM has ever seen an endpoint
// for, in ONE query.
//
// Batch rather than per-station on purpose: a directory listing N stations must not
// cost N round trips, and the per-station form already exists for the single-target
// case (LiveEndpointForStation). Stations with no endpoint are simply absent from the
// map — a missing key means "nobody has ever staffed this", which is what the caller
// wants to render, and materialising a zero row for every station in the space would
// make the map lie about which ones COMM knows.
func (s *Store) StaffingByStation(ctx context.Context) (map[string]Staffing, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT station_id, COUNT(*), COALESCE(MAX(last_seen_at),'')
  FROM endpoint
 WHERE station_id IS NOT NULL AND station_id <> '' AND revoked_at IS NULL
 GROUP BY station_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Staffing)
	for rows.Next() {
		var id string
		var st Staffing
		if err := rows.Scan(&id, &st.Endpoints, &st.LastSeenAt); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// *** LiveEndpointForStation IS DELETED — its last caller went with the channel. ***
//
// It answered "which endpoint is currently reading for this station", the single-target form of
// StaffingByStation, and the channel open path was the only thing that asked. Mail is addressed to
// a station and collected by whatever endpoint staffs it, so nothing needs to name that endpoint
// in advance any more.

// callerIsInRoom reports whether this id names a room THE CALLER IS A MEMBER OF.
//
// Membership, not existence, and the difference is a security property rather than a
// nicety. My first version asked only whether the room existed, and the test written
// alongside it caught the consequence immediately: a station that is NOT in a room got
// told "that is a ROOM", which confirms its existence. That is precisely the oracle
// comm_open_channel's uniform refusal exists to close — reopened by a helpful error
// message, which is how these usually come back.
//
// A member already knows the room exists, so telling them costs nothing. Everyone else
// gets the generic text, which mentions rooms only as a CONCEPT — true for every caller
// and informative to none of them about who is in what.
func (s *Store) callerIsInRoom(ctx context.Context, ep *Endpoint, id string) bool {
	party := endpointPartyKey(ep.ID)
	if ep.StationID != "" {
		party = stationParty(ep.StationID)
	}
	var n int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM room_member_mirror WHERE room_id=? AND party_key=?`, id, party).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
