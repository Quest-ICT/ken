package commserver

// *** THE FROZEN-SEAT DEFECT IS DELETED BY THE MODEL, AND THIS TEST WITH IT. ***
//
// It asserted that a file offer wakes the station's LIVE reader rather than the endpoint rowid
// frozen into the channel when it opened — a real bug, fixed 2026-08-20, and covered here at the
// commserver layer precisely because a test one layer below could not see it.
//
// Its premise was that a station can have several readers: "a SECOND session now staffs the same
// station. It is the live reader; the seat is not." A station has exactly ONE mailbox now, so the
// live reader and the seat are necessarily the same row and the two cannot diverge. The setup
// guard it opened with — fail if they are equal — became a permanent failure, which is the honest
// signal that the case is gone rather than untested.
//
// ken-prod-ops predicted this when the design was decided: three of the last four defects it had
// reported lived in the machinery this change deletes.
