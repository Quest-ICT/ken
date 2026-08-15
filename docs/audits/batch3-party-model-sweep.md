# Batch 3 — the endpoint-versus-party sweep
**109 sites classified, five independent lenses, every finding adversarially verified.**
Raw evidence for `docs/FINISHING.md`'s Batch 3. Recorded because the item's whole purpose is
that there is no *eighth* instance found in production — a claim that needs a list, not a memory.

## Method

Five lenses swept in parallel, each blind to the others: Go-level rowid comparisons; SQL
predicates on endpoint columns; everything OUTSIDE `internal/comm`; the schema itself; and
**the inverse bug** — places where widening endpoint to party is now wrong. Each lens's output
went to an adversarial verifier told to REFUTE, in both directions: kill a `defect` that is
unreachable or genuinely about one connection, and kill a `correct` that a replacement session
would in fact stumble over. A completeness critic then diffed the file list and asked what
modality nobody ran.

## Result
- sites classified: **109**
- verdicts surviving refutation as defects: **37** (37 verdicts over 26 unique sites; four lenses converged on one line)
- unsure: **0**

## Every site, as classified
| sev | site | lens | verdict | note |
|---|---|---|---|---|
| high | `internal/comm/channel.go:136` | go-comparisons | defect | Known site (A); re-read and it is still present exactly as described — `cur.EndpointA == ep.ID \|\| cur.EndpointB == ep.ID` with no station comparison, and the second-redeem branch below it fills the fr |
| high | `internal/comm/file.go:404` | go-comparisons | defect | Confirmed, and the blast radius is LARGER than the finder states. E2 (station S) passes the party check at 386-392 because RevokeEndpoint/SeverEndpointsBoundBy only set revoked_at and never clear stat |
| high | `internal/comm/file.go:404` | sql-predicates | defect | Confirmed, and BROADER than the finder claimed. attachment.recipient_endpoint is written as `peer` from the sender's ChannelFor (file.go:314), and ChannelFor returns the other SEAT's rowid (channel.go |
| high | `internal/comm/file.go:408` | outside-comm | defect | NOT VERIFIED - no verdict returned for this site |
| high | `internal/comm/message.go:933` | go-comparisons | defect | Known site (B); re-read and unchanged — PendingReplies filters `m.sender_endpoint=?` bound to ep.ID (message.go:935), while every other obligation-shaped read in the package keys on sender_party (noti |
| high | `internal/comm/message.go:1133` | outside-comm | defect | AGREE — confirmed, and I RAISE it to high. E2 keeps working through the station predicates while the seat still names P1; once P1 has been idle 7 days and no surviving row references it, the DELETE re |
| medium | `internal/comm/channel.go:194` | go-comparisons | defect | Confirmed, with the precondition sharpened: it needs the SEAT endpoint to have called comm_unbind (a dead-but-still-bound predecessor keeps station_id, so the normal takeover is fine — that is why the |
| medium | `internal/comm/channel.go:253` | inverse-bug | defect | The BEHAVIOUR is real and the line is reachable, so I do not refute the finding — but the rationale that made it 'high' is FALSE and I downgrade it. (1) FALSE: 'the operator's brake still targets S<-> |
| medium | `internal/comm/file.go:404` | schema-shape | defect | CONFIRMED, and the finder's dead-code argument holds under checking. RevokeEndpoint and SeverEndpointsBoundBy set revoked_at and DO NOT clear station_id (endpoint.go:329-330, 369-370), so endpointPart |
| medium | `internal/comm/file.go:404` | inverse-bug | defect | Confirmed, and it is the strongest of the file findings. `JOIN endpoint e ON e.id = a.recipient_endpoint` tests the endpoint named on the ATTACHMENT row, never the caller. The caller's own revocation  |
| medium | `internal/comm/message.go:1135` | go-comparisons | defect | Confirmed structurally and by schema. channel.endpoint_a/endpoint_b are `REFERENCES endpoint(id) ON DELETE CASCADE` (0001_init.sql:87-88) and foreign keys are enforced on every pool (comm.go:259 `_pra |
| medium | `internal/comm/message.go:1135` | sql-predicates | defect | Confirmed. The predicate itself is an honest connection-scoped question, but the ROW it deletes carries a station-scoped relationship as a cascade dependant, and nothing in the statement knows a stati |
| medium | `internal/commserver/commserver.go:661` | outside-comm | defect | AGREE with the finder — confirmed defect. E2 (station P's replacement, seat B still holding P1's rowid) parks its poll under E2.ID; a send from S resolves peer=P1 and wakes a rowid with no waiter, whi |
| medium | `internal/commserver/files.go:165` | outside-comm | defect | AGREE — confirmed defect, same shape as site 1 and reachable by the same replacement-session path: attachment.recipient_endpoint = P1 forever; P2 inherits and polls the offer, but the wake goes to the |
| medium | `internal/web/comm.go:119` | outside-comm | defect | AGREE — confirmed defect. E2, the station's only live reader, is shown with zero channels and an empty ChannelsLine, so the revoke confirm names no blast radius at all for the row whose revocation act |
| low | `internal/comm/channel.go:140` | go-comparisons | defect | The claim that the answer is wrong is right; the severity is not. E2 (station S) redeeming a still-unexpired code whose channel has both seats filled does fall past 136 and get ErrDenied, and the idem |
| low | `internal/comm/channel.go:265` | go-comparisons | defect | Confirmed and loss-free. ChannelFor returns the seat ROWID as `peer`, and commserver.go:660-670 short-circuits: `if peer != 0 { w.notify(peer) } else { WakeTargetsFor(...) }`. A parked poll registers  |
| low | `internal/comm/channel.go:282` | inverse-bug | defect | Same live-derivation root as the ChannelFor finding, same reachability, and I confirm the observable effect: after E1 unbinds and rebinds to S2, every endpoint of S2 enumerates the S<->T channel with  |
| low | `internal/comm/channel.go:625` | inverse-bug | defect | Confirmed as the mildest of the three, and only as a companion. Because ListChannels drops the channel for E2 in the same way, the count does not produce the lying-zero this function was written to fi |
| low | `internal/comm/endpoint.go:263` | go-comparisons | defect | Confirmed: the sentence is false since migration 0009, and so is the tool text built on it. Poll for an UNBOUND endpoint binds only `d.party_key = 'e:<rowid>'` (message.go:569-570), while everything t |
| low | `internal/comm/file.go:248` | go-comparisons | defect | The code shape and the divergence are real, but the severity is over-stated. Confirmed: the lookup is keyed on sender_endpoint, and UNIQUE INDEX idx_attachment_idem is (channel_id, sender_endpoint, id |
| low | `internal/comm/file.go:248` | sql-predicates | defect | The inconsistency with 0009 is real and the line is reachable by E2 on comm_file_offer, so I cannot refute it. Downgraded medium -> low: unlike the inbox/relationship sites, nothing E2 INHERITS is den |
| low | `internal/comm/file.go:248` | schema-shape | defect | CONFIRMED as an inconsistency with a rule this codebase already decided in writing: 0009 re-keyed message idempotency to (scope, sender_party) with the stated reason 'a session that reconnects under a |
| low | `internal/comm/file.go:386` | inverse-bug | defect | Not stale (this IS the ce9233e fix) and reachable. Direction A confirmed: delivery.party_key is RECORDED 's:S' and never moves, while this line RE-DERIVES the recipient's party from its current bindin |
| low | `internal/comm/message.go:283` | schema-shape | defect | CONFIRMED, but the site attribution needs correcting: line 283 applies the prescribed helper correctly; the defect is that `peer` is a seat rowid whose STATION is re-derived from a mutable binding, wh |
| low | `internal/comm/message.go:869` | inverse-bug | defect | Confirmed reachable and real: the SELECT has no claim clause, so with E1 holding a live lease on scope_seq 5, E2 (same station) polls 6 — 5 is hidden from its poll by :584-586 — and comm_ack{channel_i |
| low | `internal/comm/message.go:1137` | schema-shape | defect | CONFIRMED, and slightly stronger than stated. The guard covers attachment.sender_endpoint but not recipient_endpoint, whose FK is CASCADE, so deleting the recipient endpoint destroys the row that is ' |
| low | `internal/comm/message.go:1205` | schema-shape | defect | CONFIRMED, with a sharpened reachability statement the finder got slightly wrong by omission. A plain replacement session is NOT the trigger: revocation and death both leave station_id intact, so E2 s |
| low | `internal/comm/message.go:1205` | inverse-bug | defect | Confirmed as re-derivation: the recorded fact m.sender_party is written in the sending transaction and this SELECT ignores it, so a sender's comm_unbind blanks from_station_id on every message it ever |
| low | `internal/comm/migrations/0002_files.sql:31` | schema-shape | defect | The finding is factually right — this is the last recipient column in comm.db that stores a connection where a party belongs, and 0009 deliberately made the equivalent delivery column nullable/SET NUL |
| low | `internal/comm/migrations/0002_files.sql:63` | sql-predicates | defect | The facts check out, but the finder's causal claim is backwards and the severity does not survive it: the index does not PERMIT the duplicate — the read at file.go:248 does. Under this package's singl |
| low | `internal/comm/provenance.go:97` | schema-shape | defect | CONFIRMED, same shape and same trigger as site #6: the sender's station is re-derived from se.station_id rather than read from the frozen m.sender_party on the same row, so an unbind blanks the name a |
| low | `internal/commserver/commserver.go:661` | sql-predicates | defect | Confirmed, and it is the same shape at two more sites the finder named and I verified: commserver.go:820 notifies res.RecipientRow (OfferResult.RecipientRow is `peer` from ChannelFor, file.go:241) and |
| low | `internal/commserver/commserver.go:792` | outside-comm | defect | AGREE — confirmed, low. Nothing breaks mechanically: E2's ack works. The defect is that when acked=0 arrives for any other legitimate reason (already settled, already swept, a sibling reader of the sa |
| low | `internal/commserver/commserver.go:820` | outside-comm | defect | AGREE — confirmed defect, third instance of the identical shape, correctly scoped by the finder to the path branch. E2 inherits the offer message by party and reads it, but only after its wait elapses |
| low | `internal/commserver/files.go:165` | schema-shape | defect | CONFIRMED that E2 parked in comm_poll is not woken — the wakeup is keyed on the attachment's frozen recipient rowid — but the finder's DISTINGUISHING EVIDENCE IS WRONG, and the correction widens the f |
| low | `internal/web/templates/comm.html:141` | outside-comm | defect | AGREE — confirmed, but genuinely minor. After a takeover the column renders P1's self-chosen label or opaque id as a 'party' to a conversation P2 is conducting on the station's behalf; the operator ha |
| none | `cmd/ken/main.go:526` | outside-comm | correct | NOT VERIFIED - no verdict returned for this site |
| none | `internal/comm/admin.go:45` | go-comparisons | correct | Agreed. An operator display, not an authorisation — and it cannot dangle: channel.endpoint_a is ON DELETE CASCADE, so a channel whose seat endpoint was deleted no longer exists to be listed. The label |
| none | `internal/comm/admin.go:172` | go-comparisons | correct | Agreed. The rowid is a join key used only to reach the sender's space_id, chosen because room and broadcast messages have channel_id NULL and an inner join to channel dropped them. E1's and E2's messa |
| none | `internal/comm/channel.go:116` | go-comparisons | correct | Agreed. This is a write of who took seat A plus the migration-0008 authorisation snapshot; no decision is made from the rowid here. NULL station_a for an unbound joiner is correct — there is no link w |
| none | `internal/comm/channel.go:158` | go-comparisons | correct | Agreed. A write with its station snapshot alongside, guarded by a WHERE that repeats the state check against a concurrent RevokeChannel. The defect is in the guards that decide whether it runs (136/14 |
| none | `internal/comm/channel.go:249` | go-comparisons | correct | Agreed. The two rowid arms are a fast path that agrees with the station arms whenever the endpoint is bound, and they are REQUIRED for the unbound case: an endpoint that unbound while holding a seat s |
| none | `internal/comm/channel.go:262` | go-comparisons | correct | Agreed. `peer == 0` is 'seat B unfilled' — endpoint_b is NULL until the second join and scans into a zero via bID (channel.go:231) — which is a fact about the channel. endpoint_a is NOT NULL, so the o |
| none | `internal/comm/channel.go:279` | go-comparisons | correct | Agreed. For a bound endpoint the seat predicate is replaced wholesale by the station subquery, and a bound endpoint is always inside its own station's set, so nothing is lost by dropping the rowid for |
| none | `internal/comm/channel.go:433` | go-comparisons | correct | Agreed, and the unreachability claim checks out. CreateStationLinkRequest refuses fromStation == toStation (internal/store/station_console.go:403) and the link tables carry CHECK (station_a < station_ |
| none | `internal/comm/channel.go:433` | schema-shape | correct | Agree: unreachable with two endpoints of the same station, because AreStationsLinked(S,S) can never be true and it runs first. The guard is a backstop on the wrong axis, as the finder says, and the on |
| none | `internal/comm/channel.go:466` | go-comparisons | correct | Agreed. Seats are recorded and stations snapshotted; the only decision on this path — reuse or create — is made by openChannelsBetweenStations, which matches on station columns, so a successor on eith |
| none | `internal/comm/channel.go:499` | go-comparisons | correct | Agreed. Both call sites discard the second return (`ch, _, cerr := s.channelByPublicID(...)` at channel.go:450 and 474). No decision is made from it. |
| none | `internal/comm/channel.go:554` | go-comparisons | correct | Agreed. The query selects BY station and excludes revoked endpoints, returning the freshest live reader — after a takeover that is E2, not E1. The rowid is used only as a seat occupant, and the functi |
| none | `internal/comm/channel.go:607` | go-comparisons | correct | Agreed. Both halves are party/station scoped for a bound endpoint: the delivery predicate matches both party forms (640) and the seat predicate is replaced by the station subquery (625-627), with args |
| none | `internal/comm/channel.go:625` | sql-predicates | correct | Correct; this is the reference shape. E2 sees the inherited channel with a truthful count even though no seat holds its rowid. One adjacent observation I chased and am NOT reporting as a defect: the s |
| none | `internal/comm/channel.go:673` | go-comparisons | correct | Agreed. The party is derived (station when bound, rowid otherwise) and compared against room_member_mirror, which holds parties. E2 is judged a member by its station, so it gets the same room-vs-chann |
| none | `internal/comm/endpoint.go:129` | go-comparisons | correct | Agreed. Liveness is a property of a connection, and the throttled self-touch is what lets LiveEndpointForStation and the idle sweep distinguish E2 from E1 at all. |
| none | `internal/comm/endpoint.go:277` | inverse-bug | correct | Agree, and it is the model the other sites should follow: it splits party-scoped rows from connection-scoped ones by reading the RECORDED party_key (`party_key <> 'e:'\|\|id`) rather than re-deriving an |
| none | `internal/comm/endpoint.go:320` | go-comparisons | correct | Agreed. Per-connection leases released by the key that created them; the inbox itself is untouched because it is party-keyed, so a session rebinding under a fresh key inherits immediately. (It is also |
| none | `internal/comm/endpoint.go:362` | go-comparisons | correct | Agreed. It releases a per-connection LEASE by naming that connection, which is what makes a takeover fast instead of costing 300 s of lease. The observation that this is the same revocation that trips |
| none | `internal/comm/file.go:248` | inverse-bug | correct | Agree. An upload offer is a transfer THIS connection is about to perform and the re-armed grant is minted for the caller, so matching a predecessor's row would attach a new connection's PUT to a stale |
| none | `internal/comm/file.go:297` | go-comparisons | correct | Agreed. The count is `sender_endpoint = ep.ID`, so a successor starts at 0 and is never blocked by a predecessor's stalled offers — the failure direction is permissive, so no inheritance is denied. ma |
| none | `internal/comm/file.go:297` | sql-predicates | correct | The 'correct' verdict survives the attack in both directions. E2 binds to S, offers, and this count binds sender_endpoint=E2: it sees 0 and admits the offer, which is the right observation. Widening i |
| none | `internal/comm/file.go:297` | schema-shape | correct | Agree: concurrency bounding is rate limiting, which the brief names as a correct use of an endpoint rowid, and the error direction favours the successor (E2 starts at zero and is never blocked by E1's |
| none | `internal/comm/file.go:297` | inverse-bug | correct | Agree. This bounds the accounting hole between 'grant minted' and 'bytes counted' for the connection that will perform the PUT — rate limiting, which is on the lens's explicit per-connection list. E2  |
| none | `internal/comm/file.go:314` | go-comparisons | correct | Agreed as written: storage of the seat occupants, resolved to parties wherever it addresses anything (GrantDownload:386, CompleteUpload -> enqueueLocked:482). The one place the recipient rowid is NOT  |
| none | `internal/comm/file.go:386` | go-comparisons | correct | Agreed — this is the already-fixed sixth site and it works as documented: endpointParty(a.recipientRow) vs PartyOf(ep), with ErrNotFound rather than ErrDenied so a non-recipient cannot confirm the id  |
| none | `internal/comm/file.go:411` | go-comparisons | correct | Agreed. A grant is a one-time credential handed to one curl invocation; it is minted against the CALLER's rowid and redeemed by comparing the grant's endpoint token to the HTTP principal's token (file |
| none | `internal/comm/file.go:461` | go-comparisons | correct | Agreed. The authorisation here is at TOKEN level against the HTTP principal — a credential check, not an inbox decision — and the grant's endpoint row cannot vanish underneath it (transfer_grant.endpo |
| none | `internal/comm/file.go:497` | go-comparisons | correct | Agreed. CompleteUpload reads the recorded rowids and hands them to enqueueLocked, which resolves both to parties before addressing anything — so a successor that took over between the offer and the up |
| none | `internal/comm/message.go:160` | go-comparisons | correct | Agreed. This is a conversion, not a comparison, performed inside the writing transaction so a binding landing mid-send cannot file the message and its deliveries under different identities. Its output |
| none | `internal/comm/message.go:173` | inverse-bug | correct | Refuted. Keying on sender_party is the deliberate, documented choice and it is the one that makes the replacement case work — the exact property the sweep is protecting. The described failure needs tw |
| none | `internal/comm/message.go:265` | go-comparisons | correct | Agreed. sender_endpoint is provenance/audit written alongside sender_party; every station-scoped reader uses the party column (notice.go:76/92, room_send.go:162). The one station-scoped reader that wr |
| none | `internal/comm/message.go:276` | go-comparisons | correct | Agreed (the construct is at message.go:283 in the current tree — line drift, same code). endpointParty(peer) converts the recorded seat rowid to 's:T' for any bound peer, dead or alive, because a dead |
| none | `internal/comm/message.go:362` | go-comparisons | correct | Agreed, and independently verified as unreachable. A repo-wide grep for nextSeq/senderKey finds only the definitions at message.go:362/415 and nextSeq's own call to senderKey — no caller anywhere, tes |
| none | `internal/comm/message.go:398` | schema-shape | correct | Agree — dead detector for a condition per-scope sequencing made structurally impossible. Both traps the finder names are real and worth the comment: channel_seq/nextSeq/senderKey are an unreachable is |
| none | `internal/comm/message.go:405` | sql-predicates | correct | Correct, and dead exactly as described. E2 sending on a channel E1 already used takes the next per-SCOPE number (nextScopeSeq, party.go:160-172), so the collision this matcher explains cannot occur; t |
| none | `internal/comm/message.go:484` | go-comparisons | correct | Agreed (current lines 478 and 482). Both rowids are converted to parties before they address anything; the rowids survive only in message.sender_endpoint and delivery.recipient_endpoint, which are aud |
| none | `internal/comm/message.go:585` | sql-predicates | correct | Correct, and it is the one place a rowid comparison IS the decision. E2's observation — the row is hidden while E1's lease holds, visible once claim_expires_at lapses or an operator revokes E1 — is ri |
| none | `internal/comm/message.go:588` | go-comparisons | correct | Agreed. The rowid at 585 is the CLAIM arm only; the inbox itself is selected by party at 569-574. A claim is a lease between two readers of one station racing one inbox, and only a rowid can express ' |
| none | `internal/comm/message.go:620` | inverse-bug | correct | Agree — this is the textbook check that must stay endpoint-keyed. It decides which single CONNECTION currently holds a row, it is a lease with an expiry rather than ownership, and every path that remo |
| none | `internal/comm/message.go:630` | go-comparisons | correct | Agreed. The UPDATE is selected by `party_key = ?` (bound to the per-row deliveryParty carried from the SELECT) and the rowid appears only as the claimant and in the still-claimable disjunct. Substitut |
| none | `internal/comm/message.go:741` | go-comparisons | correct | Agreed. ep.ID lands in acked_by_endpoint, an audit column; the row SELECTION is partyPredicate(ep, "") (message.go:740, 749). E2 acking a delivery addressed to 's:S' and delivered to E1 settles it, wh |
| none | `internal/comm/message.go:742` | inverse-bug | correct | Refuted as a standalone finding. This statement decides a PARTY question — 'the station has processed message X' — which S4 says one ack settles, and it is idempotent by contract; the caller must NAME |
| none | `internal/comm/message.go:746` | sql-predicates | correct | Correct. E2 acks a row its predecessor was handed because the DECISION is party-shaped; the rowid is an audit stamp answering 'which connection performed this act', the same role recipient_endpoint pl |
| none | `internal/comm/message.go:775` | go-comparisons | correct | Agreed — this is the reference shape. Bound endpoints match both 's:<station>' and their own 'e:<rowid>' (the S7 restore-skew arm), so the second disjunct widens rather than scopes. Shared by Ack, Ack |
| none | `internal/comm/message.go:1218` | go-comparisons | correct | Agreed, and the coupling is stronger than the finder says — it is safe by TWO mechanisms, not one. message.sender_endpoint is `REFERENCES endpoint(id) ON DELETE CASCADE` (0009_delivery.sql:75), so an  |
| none | `internal/comm/migrations/0001_init.sql:87` | schema-shape | correct | Agree, and I could not break it. The self-limiting claim checks out: unread channel mail names the seat endpoint in delivery.recipient_endpoint and every message names its sender_endpoint, so no chann |
| none | `internal/comm/migrations/0002_files.sql:78` | schema-shape | correct | Agree. A single-use, minutes-lived credential for one HTTP request is the per-connection shape the brief names as correct, and nothing about an inbox, a relationship or an obligation is decided from t |
| none | `internal/comm/migrations/0009_delivery.sql:149` | schema-shape | correct | Agree. Nullable + SET NULL + audit-only + never the addressing key is exactly right, and provenance pairs it with a station-party arm so a NULL column costs nothing. Same verdict for claimed_by_endpoi |
| none | `internal/comm/migrations/0010_rooms.sql:53` | schema-shape | correct | REFUTED as a defect. Every factual claim is true — the column is written once by its own backfill, never by Go, never read, and its partial index is empty for all post-migration rows — but no replacem |
| none | `internal/comm/notice.go:161` | go-comparisons | correct | Agreed — the exported rule, with the rowid form only as the unbound fallback. Checked the surface it feeds: NoticesFor keys on m.sender_party (notice.go:76, 92) and the watermark is keyed by party, so |
| none | `internal/comm/notice.go:192` | inverse-bug | correct | Refuted. A notice is computed from `m.sender_party`, i.e. it is a fact about the STATION's outbox, so a station-keyed watermark is the consistent shape — shown once per party, exactly like claim-once  |
| none | `internal/comm/party.go:67` | go-comparisons | correct | Agreed — the prescribed conversion, resolved inside the caller's transaction. Its ErrNoRows fallback to the rowid form is conservative: it can only under-share an inbox, never widen one. |
| none | `internal/comm/party.go:122` | go-comparisons | correct | Agreed as to correctness, with one correction to the reachability note: membersOfScope is called with a ROOM scope only (room_send.go:77 passes roomScope(roomID)), so the channel branch containing thi |
| none | `internal/comm/party.go:122` | sql-predicates | correct | Verdict correct, but the finder's REACHABILITY claim is wrong and worth recording: there is no 'SendToRoom channel branch'. No caller anywhere passes a 'ch:' scope to membersOfScope, so party.go:119-1 |
| none | `internal/comm/party.go:122` | schema-shape | correct | Agree: correct by unreachability today, and the finder is right to flag it as a trap rather than a defect. Worth adding that the dormant branch has the same defect as site #8 pre-baked — it derives se |
| none | `internal/comm/party.go:236` | go-comparisons | correct | Agreed — the reference shape for turning a party into live connections at notify time: it joins on the party key in both forms, filters to queued deliveries and excludes revoked endpoints, so a room m |
| none | `internal/comm/party.go:240` | inverse-bug | correct | Agree. This resolves a recorded party to CURRENTLY live endpoints for a wakeup only: it delivers nothing and authorises nothing, it excludes revoked endpoints, and the poll it wakes re-reads the datab |
| none | `internal/comm/pending.go:86` | go-comparisons | correct | Agreed. It reuses partyPredicate and splices it into the shared fragment via %PARTY%, so both party forms are counted and no rowid comparison exists on this path. |
| none | `internal/comm/provenance.go:105` | outside-comm | correct | NOT VERIFIED - no verdict returned for this site |
| none | `internal/comm/provenance.go:107` | go-comparisons | correct | Agreed. Both arms are present and the predicate keys on the ACTOR, not the endpoint, so a delivery recorded against E1 with party 's:S' matches through either arm. Over-matching is the deliberately sa |
| none | `internal/comm/provenance.go:107` | sql-predicates | correct | Correct. E2, bound to S under the same actor, is matched by arm 2 for every delivery filed under 's:S' — including after E1's row has been swept entirely, since arm 2 resolves the party rather than th |
| none | `internal/comm/provenance.go:110` | inverse-bug | correct | Agree with the finder on the cited line. The station arm matches on d.party_key — the RECORDED party — rather than re-deriving it, and it can only add sources; the function is documented as biased tow |
| none | `internal/comm/room_send.go:111` | go-comparisons | correct | Agreed. Sender: ep.ID becomes message.sender_endpoint (audit) beside sender_party; idempotency (room_send.go:158-163) and reply correlation (194-198) both key on the party, so a successor's retry retu |
| none | `internal/comm/room_send.go:238` | go-comparisons | correct | Agreed. Room recipients carry a null Endpoint (roomMembers, party.go:195-211) and nullInt keeps the column NULL rather than 0, so nothing about a room delivery can be scoped to a connection; E2 polls  |
| none | `internal/commserver/commserver.go:358` | outside-comm | correct | AGREE with the finder: not a defect. E2 binds to S, builds 's:S', and sees exactly the rooms and audience E1 saw — the inheritance works. I could find no divergence: the only way "s:"+StationID could  |
| none | `internal/commserver/commserver.go:885` | inverse-bug | correct | Agree. Every comm_* call proves that endpoint's own secret, and both revocation facts are read AT USE rather than trusted from a mark, so a station-wide widening buys a revoked E1 nothing and E2 is un |
| none | `internal/commserver/commserver.go:940` | outside-comm | correct | AGREE with the finder: not a defect. Ownership of a credential is per-connection by construction — E2 authenticates with its own endpoint_id/secret under whatever token registered it, and never has to |
| none | `internal/commserver/files.go:183` | outside-comm | correct | AGREE with the finder: not a defect. This is credential authentication for one HTTP request. E2 mints its own single-use grant against its own rowid and presents its own bearer token, so the compariso |
| none | `internal/commserver/waiters.go:45` | outside-comm | correct | AGREE with the finder: not a defect, and the reasoning is right for the right reason. The parked goroutine IS one connection, so the map key must be a connection. E2 parks under its own rowid and is w |
| none | `migrations/0015_voucher_nominates_endpoint.sql:38` | schema-shape | correct | Agree. A voucher is the credential that ADMITS one connection to a station, so naming that connection is the point — the migration says so, and redeeming it additionally requires that endpoint's own s |

## Completeness critic

```
SWEEP-COMPLETENESS REPORT — internal/comm, endpoint-rowid-vs-party class
Working tree: <repo> @ a8221f3 (main, clean). Read-only; no files written, no tests run.

════════════════════════════════════════════════════════════════════
1. FILE DIFF — WHAT NO LENS EVER OPENED
════════════════════════════════════════════════════════════════════

internal/comm holds 14 non-test .go files and 11 migrations. Ten .go files and
four migrations appear in the merged result. The remainder, opened and classified:

internal/comm/comm.go (503 lines) — GENUINELY CLEAN, not unswept.
  Contains no query that names an endpoint. `grep -n "endpoint" comm.go` returns 7 hits,
  all prose (sentinel doc comments at :58-72, Limits doc at :141/:181/:205, sha256Hex at
  :474). The only Store methods are Close/Limits/SetLimits/lim/Migrate/appliedVersions/
  tx/Path. Nothing here decides an inbox, a relationship or an obligation.

internal/comm/room_mirror.go (153 lines) — GENUINELY CLEAN of the
  rowid class, but see finding N4 below. Every one of its five methods takes or writes a
  PARTY STRING; the word "endpoint" does not appear in the file. ReplaceRoomMirror,
  MirrorEpoch, RoomsForParty, RoomsFor, BroadcastAudience all key on party_key.

internal/comm/diskfree_unix.go (17 lines) / diskfree_other.go (9 lines) — statfs wrappers,
  no database access. Trivially clean.

Migrations never mentioned: 0003, 0004, 0005, 0006, 0007, 0011.
  0003_seq_and_status.sql:28-36 — creates channel_seq(channel_id, sender_endpoint), the
    dead island already noted at message.go:362; rebuilt by 0007 onto sender_key.
  0006_station_binding.sql:60,70 — adds message.claimed_by_endpoint and
    idx_message_claim ON message(recipient_endpoint, claim_expires_at). BOTH died with the
    table when 0009 rebuilt `message` (0009 has no seq, no recipient_endpoint, no
    claimed_by_endpoint on message; the claim moved to `delivery`). No live code names them.
  0004, 0005 — channel label and secret rotation; no endpoint-keyed decision column.
  0007 — introduces the 'e:'/'s:' tagged party namespace; this is the ancestor of the
    correct shape, not a defect.
  0011 — notice_watermark keyed on party_key, explicitly for the successor case (:46-47).

Conclusion on Q1: the five lenses covered every file that contains an endpoint-rowid
decision. No file was skipped because it was overlooked.

════════════════════════════════════════════════════════════════════
2. MODALITIES NOBODY RAN
════════════════════════════════════════════════════════════════════

Four. Three produced findings.

M1. THE INVARIANT-BYPASS MODALITY (nobody ran it; produced the strongest new finding).
    The lenses asked "does this line compare a rowid?". Nobody asked "what invariant does
    a correct line REST on, and is that invariant actually enforced?" See N1.

M2. THE GUIDANCE / FROZEN-INSTRUCTIONS MODALITY (run only incidentally).
    The outside-comm lens caught one string (commserver.go:791) by accident while reading
    Ack. Nobody swept the instruction block and tool descriptions systematically for the
    pre-station rule. See N2. This matters more than an ordinary comment because
    MCP instructions and tool descriptions pin at conversation start — a wrong sentence
    there is unfixable for every session already running.

M3. THE FROZEN-HEURISTIC MODALITY (nobody ran it).
    Several lenses classified LiveEndpointForStation "correct" on its own doc's argument.
    Nobody asked what happens when an explicitly approximate answer is written into a
    durable row and then consumed as an identity. See N3.

M4. GIT-HISTORY SIBLING CHECK (I ran it; NO new site).
    Traced each of the six fixes and re-grepped the current tree for the same predicate
    shape. 2d045c5 (Poll/Ack/Sweep/provenance), 5d638ba+a090f57 (waiting_for_you →
    queuedForEndpoint, message.go:802, now partyPredicate), 9ef2b9b (pending counters),
    ce9233e (GrantDownload). Each fix's siblings within its own surface are all present in
    the merged result. The one thing the history DOES add: docs/FINISHING.md:238 records
    this sweep as an open Batch 3 item, and :241 records `attachment.scope_id` as a known
    unfinished migration — so the schema-lens finding on 0010 is already tracked upstream
    and should not be re-raised as new.

Also checked and empty: metric labels (internal/metrics/metrics.go has no endpoint-keyed
series; cmd/ken/main.go:999-1004 emits space-level scalars only, no labels), and ken.db
(`grep -n endpoint migrations/*.sql` → only station_binding_voucher.redeemed_by_endpoint
and .issued_for_endpoint, both credential-shaped and already covered).

════════════════════════════════════════════════════════════════════
3. NEW SITES
════════════════════════════════════════════════════════════════════

────────────────────────────────────────────────────────────────────
N1. THE "AN ENDPOINT CANNOT MOVE BETWEEN STATIONS" INVARIANT IS ASSERTED,
    RELIED ON, AND TRIVIALLY BYPASSED BY THE TOOL SITTING 30 LINES BELOW IT.
    internal/commserver/commserver.go:262-270
    internal/comm/endpoint.go:190-192, :198, :236
    Severity: MEDIUM as a defect in its own right; it is the RELIABILITY ARGUMENT the
    already-confirmed ChannelFor defect (channel.go:194) rests on.

commserver.go:267-269:
    if ep.StationID != "" {
        return nil, bindOut{}, errors.New("this endpoint is already bound to a station — an endpoint cannot move between stations, "+
            "because it would carry the first station's unread mail into the second. Register a new endpoint if you need a different station")
    }
endpoint.go:190-192 states the same as a design invariant: "Binding is set once at
registration and never changed — an endpoint that could move between stations would let a
session carry another station's unread mail across, which is the shared-inbox failure in a
new costume."

comm_unbind (commserver.go:296-305) clears station_id, and its own SUCCESS NOTE says
"You can bind again later." BindEndpointToStation's gate is `station_id IS NULL`
(endpoint.go:198 and :236), which the unbind satisfied. So unbind-then-bind moves an
endpoint between stations, and the tool that performs the bypass advertises it.

REPLACEMENT-SESSION SCENARIO. Station S, endpoint E1, seat A of channel C with station T.
E1 is alive; it calls comm_unbind (the remediation commserver.go's ErrSequenceCollision
branch prescribes by name), then takes a voucher for S2 from its own human's /station
console — same actor, no peer involvement — and calls comm_bind. E1 dies. E2 binds to S
and polls. E2 RECEIVES C's mail, because deliveries were filed 's:S' at write time. E2
cannot reply, cannot offer a file, cannot AckUpTo: ChannelFor derives stnA from the LIVE
join at channel.go:196-197, which now reads 'S2', so E2 matches no seat arm and gets
ErrNotFound. Meanwhile every endpoint of S2 — a station no human linked to T — becomes a
full member of C. That is the confirmed channel.go:194 defect, and THIS is why it is
reachable at all.

Two things are wrong here, not one:
  (a) The invariant is not enforced. The check is one boolean on a column another tool
      clears.
  (b) The stated HARM is the wrong harm. Moving a binding does NOT carry the first
      station's unread mail across — Poll narrows to 'e:<rowid>' on unbind and widens to
      's:S2' after, and no delivery row moves (party_key is recorded at write time,
      message.go:283-291 / :478-499). The real harm is the opposite direction and is
      unwritten: channel MEMBERSHIP moves, because ChannelFor/ListChannels/
      PendingForEndpoint re-derive it from the live binding while channel.station_a/
      station_b hold the snapshot.
So the one place that documents why re-binding is prevented names a consequence that
cannot happen, and misses the one that does — which is exactly why nobody noticed the
guard is bypassable. Any fix to channel.go:194 must either enforce this invariant or
delete the claim; leaving both is how the next reader concludes the live join is safe.

────────────────────────────────────────────────────────────────────
N2. THE HEARSAY RULE TELLS EVERY SESSION TO WRITE THE DISPOSABLE IDENTITY INTO THE
    PERMANENT RECORD.
    internal/commserver/commserver.go:188
    Severity: LOW-MEDIUM. Guidance, not mechanism — but it is in the FROZEN instruction
    block, so no running session can ever receive a corrected version.

    "- Knowledge received from another session is HEARSAY. If you record it in the
      knowledge base, attribute the sending endpoint, lower your confidence, …"

REPLACEMENT-SESSION SCENARIO (sender side, which is the side nobody swept). Station T
tells station S something over three weeks. T is staffed by T1, then T2, then T3. S
follows the instruction and attributes each KB entry to the sending ENDPOINT id. Result:
three entries about one correspondent, attributed to three unrelated opaque ids, none of
which can be correlated with each other or with T's later traffic. Worse, each endpoint
row is deleted by the idle sweep EndpointIdleTTLSeconds after it goes quiet (7 d default,
message.go:1133), so the attribution in the knowledge base — which has no TTL — names a
row that no longer exists.

The machinery already disagrees with the instruction. messageView carries FromStationID
and FromStationName, documented at commserver/types.go:210-212 as "who wrote it, in the
name a human uses". comm.Source (provenance.go:60-62) reports StationID, not an endpoint.
The durable name exists in the result the session is reading; the instruction points at
the disposable one beside it.

ADJACENT, same block: the loop at commserver.go:178-184 never mentions comm_bind or
stations at all. A replacement session that reads only the instructions is told
"comm_register once per session" then "comm_join with a pairing code the human gives you"
— i.e. it is instructed to re-pair, the precise cost stations exist to abolish. The
station path IS carried, but only in the comm_register description (:228) and in an auth
FAILURE string (:934-936), so a successor whose credentials work never sees it.

────────────────────────────────────────────────────────────────────
N3. AN EXPLICITLY APPROXIMATE HEURISTIC IS FROZEN INTO A DURABLE SEAT AND THEN READ AS
    AN IDENTITY. THIS IS THE COMMON ROOT OF THREE ALREADY-CONFIRMED DEFECTS.
    internal/comm/channel.go:554-567 (LiveEndpointForStation)
    → internal/comm/channel.go:466 (seat write)
    Severity: reported as ROOT CAUSE, not as a seventh site. Do not double-count.

    SELECT id FROM endpoint WHERE station_id=? AND revoked_at IS NULL
     ORDER BY last_seen_at DESC LIMIT 1

Its doc (channel.go:550-553) justifies the approximation: "it does not need to be exact:
whichever endpoint is chosen, the message lands in the STATION's inbox and any reader can
claim it." That justification is TRUE for addressing and FALSE for everything else the
value reaches, and the doc does not say which. The chosen rowid is written to
channel.endpoint_b at :466 and NEVER updated — and OpenLinkedChannel reuses an existing
channel by STATION PAIR (:441-448), so the seat chosen at the very first open outlives
every session on both sides, permanently.

That single frozen value is then consumed as an identity by:
  • ChannelFor's `peer` return (channel.go:250/252) → commserver.go:661 w.notify(peer),
    which is the confirmed stale-wakeup defect;
  • OfferFile's recipient stamp (file.go:314, `peer` from file.go:235) → the confirmed
    permanent ErrChannelClosed at file.go:404, which is permanent for the CHANNEL rather
    than for one attachment precisely because the seat never moves;
  • Sweep's cascade (message.go:1133 + 0001_init.sql:87-88), which destroys the channel
    when that one frozen rowid ages out.
Answering the parent's ordering question directly: this is the one place where "which
endpoint of a station" is decided by an ORDER BY … LIMIT 1, and the answer is durable.

────────────────────────────────────────────────────────────────────
N4. THE FOUR PENDING COUNTERS DO NOT AGREE, IN THE FILE THAT SAYS THEY MUST.
    internal/comm/room_mirror.go:96-104 (RoomsFor)
    internal/comm/channel.go:634-643 (PendingForEndpoint)
    vs internal/comm/pending.go:49-56 (pendingScopeSQL)
    Severity: LOW. Adjacent to the class, not an instance of it.

pending.go:10-16 names four counters and says "THEY MUST NOT DISAGREE". pendingScopeSQL
carries three predicates and states why the expiry one is required (:38-40): "an expired
message is not deliverable, and the sweeper may not have flipped its delivery to
'expired' yet. Without this the number is right only immediately after a sweep."

Neither per-scope counter has that predicate:
  RoomsFor      — WHERE m.scope_id='r:'||room AND d.party_key=? AND d.state='queued'
  PendingForEndpoint — ON (d.party_key=? OR d.party_key=?) AND d.state='queued'
Both PendingTotalFor and BroadcastPendingFor go through pendingScopeSQL and DO have it.

Effect, and it is directly visible in ONE result: comm_channels returns pending_total
(filtered) alongside per-channel counts (unfiltered, channel.go:634) and per-room counts
(unfiltered, commserver.go:578 → RoomsFor). Between a message expiring and the next sweep
(janitor cadence ~60 s), a session can read pending_total=0 with a per-channel or per-room
count of 1 — in the survey the frozen instruction block tells it to read first
("Read pending_total FIRST … the per-channel and per-room counts beside it say where",
commserver.go:183). Bounded by the sweep interval; over-reporting, never under.

Second, LATENT: RoomsFor binds a SINGLE party (`d.party_key = ?`) where the other three
use partyPredicate's two forms. pending.go:84-85 already names this — "RoomsFor takes a
single party and that is precisely its blind spot" — and it is currently harmless only
because internal/store/rooms.go:207 emits `"s:"+stationID` exclusively, so no 'e:' party
can ever be a room member. It becomes live the moment anything puts an endpoint-form party
in the mirror.

────────────────────────────────────────────────────────────────────
N5. LEAK: station_me RETURNS THE STATION'S ENDPOINTS PREDECESSOR-FIRST, UNDER A FIELD
    NAME THAT ASKS A SINGULAR QUESTION.
    internal/comm/endpoint.go:464 (`ORDER BY id`)
    internal/stationserver/types.go:84-94
    internal/stationserver/stationserver.go:838-844
    Severity: LOW. No authorization consequence.

    SELECT endpoint_id FROM endpoint WHERE station_id=? AND revoked_at IS NULL ORDER BY id

REPLACEMENT-SESSION SCENARIO. E1 dies without being revoked — the ordinary case; death
does not set revoked_at, only an operator does. E2 binds to S and calls station_me, the
call every session is instructed to make first. It gets a two-element list with E1 FIRST,
because rowid order is registration order. The field's own doc (types.go:84-85) frames it
singularly — "the answer to 'which endpoint_id should I be calling comm_* with?'" — while
the jsonschema (:94) frames it as a membership test, which is the correct and only
answerable form: station_me is authenticated on the /station surface and has no comm
endpoint identity, so the server genuinely cannot know which one the caller holds.
A session that reads the singular framing and takes the first id adopts its predecessor's,
for which it has no secret, and lands on the auth error at commserver.go:930 — whose text
offers "may never have existed / secret may be wrong / may have been swept", none of which
is what happened. Fix is wording plus ordering (newest first would at least make the
likely-correct id the first one), not a predicate change.

════════════════════════════════════════════════════════════════════
4. WHAT I LOOKED FOR AND DID NOT FIND
════════════════════════════════════════════════════════════════════

• Unscoped reads. MessageByID (message.go:963) takes no *Endpoint and applies no party
  filter — but its only caller is PendingReplies (message.go:953); no MCP or HTTP surface
  reaches it. Not a leak.
• Reply/obligation correlation. Both send paths correlate replies on senderParty
  (message.go:229-231, room_send.go:198) and record delivery.replied_by by party
  (message.go:316-318). answered_at is a message-level roll-up. Correct shape throughout;
  PendingReplies (known site B) is the only obligation read that is not.
• Delivery-clock arming. Poll arms reply_deadline_at and first_delivered_at on the
  delivery row keyed by deliveryParty (message.go:682-692), so a successor arms the
  station's clock correctly.
• Notices. notice.go is party-keyed end to end — sender_party at :76 and :92, watermark
  keyed by party at :192/:204/:224, recipients named by d.party_key at :140. No rowid
  anywhere in the file.
• Metric labels. None keyed by endpoint (see M4).
• ken.db. No station-scoped relationship keys off an endpoint; only the two voucher
  columns, both credential-shaped.
• The ep-taking surface is exhaustively enumerated: 19 Store methods take *Endpoint
  (JoinChannel, ChannelFor, ListChannels, OpenLinkedChannel, PendingForEndpoint,
  callerIsInRoom, Send, Poll, Ack, AckUpTo, cumulativeAckScope, PendingReplies,
  SendToRoom, Broadcast, OfferFile, GrantDownload, PendingTotalFor, BroadcastPendingFor,
  countPending). Every one appears in the merged result. The two that use neither a party
  helper nor a station comparison are exactly the two known sites, A and B.

════════════════════════════════════════════════════════════════════
5. BOTTOM LINE
════════════════════════════════════════════════════════════════════

The five-lens sweep is complete AS A SWEEP OF COMPARISONS. It is not complete as a sweep
of the class, because the class also lives in (a) invariants that are asserted but not
enforced — N1, which is what makes the confirmed channel.go:194 defect reachable at all;
(b) guidance that a session acts on and cannot receive a correction to — N2; and (c) an
approximate answer frozen into a durable row — N3, which is the single upstream cause of
three separately-reported downstream defects and should be fixed once rather than three
times.

If only one thing from this report is acted on, make it N1: it is a one-boolean guard
protecting a security property, bypassed by the adjacent tool's documented happy path,
with a written justification that names the wrong consequence.
```
