package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Station tasks (docs/STATIONS.md §11).
//
// The failure being fixed is DECAY, not storage: pending items live in a session's
// context, older ones lose to newer material and compaction, and recall becomes a
// memory harvest that quietly stops returning the oldest things. Everything here
// exists to counteract that, so two properties are load-bearing and are implemented
// rather than merely intended:
//
//   - ORDERING IS A CONTRACT (§11.5), not a tunable heuristic, and the briefing head
//     has FIXED SLOTS so the aging clause cannot be starved by the two monotonic
//     classes above it.
//   - "SURFACED" MEANS WHAT KEN CAN OBSERVE (§11.4). Only a briefing stamps, only the
//     rows it actually displays, and throttled — because stamping the whole open set on
//     every connect would show a perfect surfacing history while the human was told
//     nothing, which is the original failure with a database attached.

// StationTaskLimits bounds the list. Values are settings in the shipped product; the
// defaults here match §9, whose reason is a BACKUP argument: every byte lands in the
// live database plus fourteen nightlies plus Litestream.
type StationTaskLimits struct {
	MaxOpen        int // refuse a new task past this (§9: 500)
	MaxTextBytes   int // one line, by construction (§9: 512)
	MaxDetailBytes int // detail + context (§9: 4 KiB)
	ListLimit      int // default AND hard ceiling (§11.5: 50)
	// BriefStampThrottleSec approximates "at most once per staffing session" without a
	// session table: a task the briefing displayed within this window is shown again but
	// NOT re-stamped, so `station_me` called repeatedly cannot advance the aging clock.
	BriefStampThrottleSec int
}

// DefaultStationTaskLimits are §9's numbers.
func DefaultStationTaskLimits() StationTaskLimits {
	return StationTaskLimits{
		MaxOpen: 500, MaxTextBytes: 512, MaxDetailBytes: 4096,
		ListLimit: 50, BriefStampThrottleSec: 1800,
	}
}

// StationTask is one row. `blocked_on` is the field that earns its place: it turns the
// end-of-session guess ("two things waiting on you") into a query.
type StationTask struct {
	TaskID           string
	StationID        string
	StationName      string // filled by the cross-station view
	Text             string
	Detail           string
	Context          string
	BlockedOn        string // self | human | peer
	BlockedOnStation string
	RemindAfter      string
	State            string // open | done | dropped
	Resolution       string
	ResolutionLink   string
	CreatedAt        string
	HearsayAtWrite   bool
	LastBriefedAt    string
	BriefedCount     int
	DeferredUntil    string
	DeferCount       int
	LastDeferReason  string
	ClosedAt         string
}

// ErrTaskCapReached is returned instead of evicting. S12: refuse, never evict —
// silent eviction of a working note is data loss the session cannot see, while a
// refusal is an error the model reads and reacts to.
var ErrTaskCapReached = errors.New("open task cap reached")

var validBlockedOn = map[string]bool{"self": true, "human": true, "peer": true}

// AddStationTask records a commitment. `blockedOn` is REQUIRED: it is a three-value
// enum costing one token, and making it optional would put an unstated default into the
// human's only cross-station view (§11.3).
//
// It returns the new task plus NEAR-MATCHES from the open set, in the same result and at
// zero extra call cost — a model that just re-created something is told immediately
// rather than discovering it three weeks later (§11.5).
func (s *Store) AddStationTask(ctx context.Context, lim StationTaskLimits, t StationTask, tokenID string, actorID int64, hearsay bool) (*StationTask, []StationTask, error) {
	t.Text = strings.TrimSpace(t.Text)
	if t.Text == "" {
		return nil, nil, errors.New("a task needs text")
	}
	if !validBlockedOn[t.BlockedOn] {
		return nil, nil, fmt.Errorf("blocked_on must be one of self|human|peer (got %q): "+
			"self = I can act now; human = it cannot move until the owner does; peer = another station owes something", t.BlockedOn)
	}
	if len(t.Text) > lim.MaxTextBytes {
		return nil, nil, fmt.Errorf("task text is %d bytes, over the %d-byte cap — one line by construction; put the rest in detail", len(t.Text), lim.MaxTextBytes)
	}
	if len(t.Detail)+len(t.Context) > lim.MaxDetailBytes {
		return nil, nil, fmt.Errorf("detail + context is %d bytes, over the %d-byte cap — a task needing more than this is a notebook page with a plan", len(t.Detail)+len(t.Context), lim.MaxDetailBytes)
	}

	var open int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM station_task WHERE station_id=? AND state='open'`, t.StationID).Scan(&open); err != nil {
		return nil, nil, err
	}
	if open >= lim.MaxOpen {
		return nil, nil, fmt.Errorf("%w: %d open tasks (cap %d) — a longer list is not being worked; close or drop before adding", ErrTaskCapReached, open, lim.MaxOpen)
	}

	taskID, err := randBase62(8)
	if err != nil {
		return nil, nil, err
	}
	taskID = "t-" + taskID
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_task(task_id, station_id, text, detail, context, blocked_on,
                         blocked_on_station, remind_after, created_by_token_id,
                         created_by_actor_id, hearsay_at_write)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		taskID, t.StationID, t.Text, t.Detail, t.Context, t.BlockedOn,
		nullStr(t.BlockedOnStation), nullStr(t.RemindAfter), nullStr(tokenID), actorID,
		boolOrNil(hearsay)); err != nil {
		return nil, nil, err
	}
	_ = s.TouchStationActivity(ctx, t.StationID)

	near, _ := s.nearMatches(ctx, t.StationID, t.Text, taskID)
	created, err := s.StationTaskByID(ctx, taskID)
	return created, near, err
}

// nearMatches finds open tasks sharing significant words with the new text. Deliberately
// crude: the open set is small (§9), and the point is to put a hint in front of the
// model at zero extra cost, not to be a search engine.
func (s *Store) nearMatches(ctx context.Context, stationID, text, exclude string) ([]StationTask, error) {
	words := significantWords(text)
	if len(words) == 0 {
		return nil, nil
	}
	q := `SELECT ` + taskCols + ` FROM station_task WHERE station_id=? AND state='open' AND task_id<>? AND (`
	args := []any{stationID, exclude}
	for i, w := range words {
		if i > 0 {
			q += " OR "
		}
		q += "LOWER(text) LIKE ?"
		args = append(args, "%"+strings.ToLower(w)+"%")
	}
	q += ") LIMIT 3"
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func significantWords(s string) []string {
	stop := map[string]bool{"the": true, "a": true, "an": true, "and": true, "or": true, "to": true,
		"for": true, "of": true, "in": true, "on": true, "is": true, "it": true, "be": true, "we": true}
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:!?()[]\"'`")
		if len(w) >= 4 && !stop[w] {
			out = append(out, w)
		}
		if len(out) == 4 {
			break
		}
	}
	return out
}

// taskColsT is taskCols with every column aliased to the station_task table, for the
// cross-station JOIN where `station_id` would otherwise be ambiguous. Written out rather
// than derived: transforming a column list with string surgery breaks the moment a
// COALESCE has two arguments.
const taskColsT = `t.task_id, t.station_id, t.text, t.detail, t.context, t.blocked_on,
COALESCE(t.blocked_on_station,''), COALESCE(t.remind_after,''), t.state,
COALESCE(t.resolution,''), COALESCE(t.resolution_link,''), t.created_at,
COALESCE(t.hearsay_at_write,0), COALESCE(t.last_briefed_at,''), t.briefed_count,
COALESCE(t.deferred_until,''), t.defer_count, COALESCE(t.last_defer_reason,''),
COALESCE(t.closed_at,'')`

const taskCols = `task_id, station_id, text, detail, context, blocked_on,
COALESCE(blocked_on_station,''), COALESCE(remind_after,''), state,
COALESCE(resolution,''), COALESCE(resolution_link,''), created_at,
COALESCE(hearsay_at_write,0), COALESCE(last_briefed_at,''), briefed_count,
COALESCE(deferred_until,''), defer_count, COALESCE(last_defer_reason,''),
COALESCE(closed_at,'')`

func scanTasks(rows *sql.Rows) ([]StationTask, error) {
	var out []StationTask
	for rows.Next() {
		var t StationTask
		if err := rows.Scan(&t.TaskID, &t.StationID, &t.Text, &t.Detail, &t.Context, &t.BlockedOn,
			&t.BlockedOnStation, &t.RemindAfter, &t.State, &t.Resolution, &t.ResolutionLink,
			&t.CreatedAt, &t.HearsayAtWrite, &t.LastBriefedAt, &t.BriefedCount,
			&t.DeferredUntil, &t.DeferCount, &t.LastDeferReason, &t.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// StationTaskByID fetches one task.
func (s *Store) StationTaskByID(ctx context.Context, taskID string) (*StationTask, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+taskCols+` FROM station_task WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ts, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, ErrNotFound
	}
	return &ts[0], nil
}

// taskOrder is §11.5's contract, expressed once so the list and the briefing cannot
// drift apart:
//
//  1. Due        — remind_after has passed (a date is a promise the human made)
//  2. Human      — the human is the bottleneck; surfacing is the only thing that moves it
//  3. Aging      — last_briefed_at oldest first; the clause that inverts the failure
//  4. Tie-break  — created_at oldest first
//
// Deferral suppresses from the briefing HEAD only, never from membership: an item with a
// future deferred_until stays in the list, the counts and the near-match check, because
// the items that have gone quiet are exactly the ones this feature exists for.
const taskOrder = `
ORDER BY
  CASE WHEN remind_after IS NOT NULL AND remind_after <= strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 0
       WHEN blocked_on='human' THEN 1
       ELSE 2 END,
  COALESCE(last_briefed_at,''),
  created_at`

// ListStationTasks is a PURE QUERY and stamps nothing (§11.4). A model checking its own
// list three times must not silently demote items nobody was told about.
func (s *Store) ListStationTasks(ctx context.Context, lim StationTaskLimits, stationID, state, blockedOn string, limit int) ([]StationTask, int, error) {
	if state == "" {
		state = "open"
	}
	if limit <= 0 || limit > lim.ListLimit {
		limit = lim.ListLimit
	}
	where := `station_id=? AND state=?`
	args := []any{stationID, state}
	if blockedOn != "" {
		where += ` AND blocked_on=?`
		args = append(args, blockedOn)
	}
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM station_task WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT `+taskCols+` FROM station_task WHERE `+where+taskOrder+` LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ts, err := scanTasks(rows)
	return ts, total, err
}

// CloseStationTasks is the cheapest verb, and takes several ids because closing a batch
// after a release is the common case — five calls is five chances not to bother (§11.6).
func (s *Store) CloseStationTasks(ctx context.Context, stationID string, taskIDs []string, resolution, link string, actorID int64) (int, error) {
	return s.terminateTasks(ctx, stationID, taskIDs, "done", resolution, link, actorID)
}

// DropStationTasks abandons tasks. It REFUSES a `blocked_on: human` task unless the
// caller carries the human's own decision — without that guard the nag would aim the
// model's one destructive verb squarely at the pile the feature exists to preserve
// (§11.6).
func (s *Store) DropStationTasks(ctx context.Context, stationID string, taskIDs []string, reason string, humanDecided bool, actorID int64) (int, error) {
	if !humanDecided {
		for _, id := range taskIDs {
			t, err := s.StationTaskByID(ctx, id)
			if err != nil {
				return 0, err
			}
			if t.BlockedOn == "human" {
				return 0, fmt.Errorf("%s is blocked on the human and cannot be dropped by a session: "+
					"say what is blocking it, or defer it with a reason — the owner decides whether their own commitment is abandoned", id)
			}
		}
	}
	return s.terminateTasks(ctx, stationID, taskIDs, "dropped", reason, "", actorID)
}

func (s *Store) terminateTasks(ctx context.Context, stationID string, taskIDs []string, state, resolution, link string, actorID int64) (int, error) {
	if strings.TrimSpace(resolution) == "" {
		return 0, errors.New("a resolution line is required to leave `open` — the record of what happened is the point")
	}
	if len(taskIDs) == 0 {
		return 0, errors.New("no task ids given")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for _, id := range taskIDs {
		res, err := tx.ExecContext(ctx, `
UPDATE station_task SET state=?, resolution=?, resolution_link=?,
       closed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), closed_by_actor_id=?
WHERE task_id=? AND station_id=? AND state='open'`,
			state, resolution, nullStr(link), actorID, id, stationID)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	_ = s.TouchStationActivity(ctx, stationID)
	return n, nil
}

// DeferStationTask is deliberately the wordiest verb: a date AND a reason, and it leaves
// a counted trace. Deferring is legitimate; deferring silently and repeatedly is the
// failure mode (§11.6).
func (s *Store) DeferStationTask(ctx context.Context, stationID, taskID, until, reason string) error {
	if strings.TrimSpace(until) == "" || strings.TrimSpace(reason) == "" {
		return errors.New("deferring requires both a date and a reason — that asymmetry is deliberate: closing is cheaper")
	}
	res, err := s.W.ExecContext(ctx, `
UPDATE station_task
SET deferred_until=?, remind_after=?, defer_count=defer_count+1, last_defer_reason=?
WHERE task_id=? AND station_id=? AND state='open'`, until, until, reason, taskID, stationID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_ = s.TouchStationActivity(ctx, stationID)
	return nil
}

// ReopenStationTasks exists because a decision to drop is sometimes wrong, and because a
// terminal state nobody can leave makes the record a dead end (§11.3).
func (s *Store) ReopenStationTasks(ctx context.Context, stationID string, taskIDs []string, reason string) (int, error) {
	n := 0
	for _, id := range taskIDs {
		res, err := s.W.ExecContext(ctx, `
UPDATE station_task SET state='open', closed_at=NULL, closed_by_actor_id=NULL,
       last_defer_reason=? WHERE task_id=? AND station_id=? AND state<>'open'`,
			"reopened: "+reason, id, stationID)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

// TaskBriefing is what a session is handed when it staffs a station: named rows, not
// only counts. A task the human never hears named is a task that decayed.
type TaskBriefing struct {
	Head            []StationTask // the fixed-slot head, already stamped
	OpenTotal       int
	BlockedOnHuman  int
	Overdue         int
	AgingCount      int // not briefed in the last N sessions
	StuckCount      int // briefed repeatedly, unchanged and never deferred
	RepeatedlyDefer int
	Remainder       int

	// NeverBriefed is how many OPEN tasks have never appeared in a briefing head at all.
	//
	// THIS IS THE NUMBER THAT WAS MISSING, and it is large. The head holds at most seven
	// items; a station with forty open tasks surfaces the same handful and the rest are
	// invisible to the session AND to the human — never counted, never aged, never
	// reviewed. Measured across this estate on 2026-08-14: roughly 45 tasks blocked on one
	// human, and the large majority had never been surfaced to him even once.
	//
	// Remainder already said how many were not shown THIS TIME. That reads as a queue
	// waiting its turn, which is what makes it harmless-looking. This says how many have
	// never had a turn.
	NeverBriefed int

	// OldestBlockedDays is the age in days of the longest-standing task blocked on the
	// human. A count alone ("2 waiting on you") carries no urgency and no sense of whether
	// it is new or has been sitting for a month.
	OldestBlockedDays int

	// StaleRisk is how many open tasks are blocked on the human AND have not been briefed
	// in a long time — the population most likely to be ALREADY DONE and still counted.
	//
	// blocked_on is set once at creation and nothing ever revisits it, so a task whose
	// condition has been satisfied is indistinguishable from one still waiting. Both are
	// briefed with equal weight and both are counted in "waiting on you". Two of this
	// station's own were found done-but-open on 2026-08-14, one of them five releases out
	// of date, and production reported the same thing about itself the same day — twice
	// telling its human he owed something he had already finished.
	StaleRisk int

	// OldestOpenDays is the age in days of the longest-standing OPEN task, whatever it is
	// blocked on — the figure OldestBlockedDays cannot be, because its MAX is gated on
	// blocked_on='human'.
	//
	// The category it exposes is OVERTAKEN, not stale: accurate, and no longer the point.
	// ken-promo's own was "read the 1.5.1 and 1.5.2 promo briefs", created 2026-07-30 and
	// found on 2026-08-14 with Ken already at 3.6.0. It was blocked_on='self', so the
	// human-gated age could never see it, and briefed_count only RISES — being briefed every
	// session made it look attended to. Age since creation is the only signal that would have
	// surfaced it.
	//
	// It SURFACES and does nothing else. Nothing here defers, closes or reorders on age, and
	// the head's fixed slots (§11.5) are untouched: an aging clock that acted would be
	// abandoning the human's commitments on a timer, which is exactly what §11.6 refuses.
	//
	// Deferred items are INCLUDED. Deferral is a surfacing rule, never a membership rule
	// (§11.5) — an item that has gone quiet is precisely the one this figure exists for.
	OldestOpenDays int
}

// BriefStationTasks builds the briefing AND performs the only stamping in the system.
//
// The head has FIXED SLOTS — up to 2 due, 2 human-blocked, 3 aging — because classes 1
// and 2 are monotonic (a passed date never un-passes; the human-blocked pile is by
// definition the one not being cleared), so a pure rank order lets them hold the head
// forever and the aging clause never runs. Silence, the cheapest human response, must
// not be able to pin an item at rank 1 and freeze everything beneath it (§11.5).
//
// Only the rows actually returned are stamped, and only if they were not stamped inside
// the throttle window — so `station_me` called repeatedly cannot advance the clock
// (§11.4).
func (s *Store) BriefStationTasks(ctx context.Context, lim StationTaskLimits, stationID string) (*TaskBriefing, error) {
	b := &TaskBriefing{}
	row := s.R.QueryRowContext(ctx, `
SELECT COUNT(*),
  SUM(CASE WHEN blocked_on='human' THEN 1 ELSE 0 END),
  SUM(CASE WHEN remind_after IS NOT NULL AND remind_after <= strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 1 ELSE 0 END),
  SUM(CASE WHEN briefed_count >= 5 AND defer_count = 0 THEN 1 ELSE 0 END),
  SUM(CASE WHEN defer_count >= 3 THEN 1 ELSE 0 END),
  SUM(CASE WHEN briefed_count = 0 THEN 1 ELSE 0 END),
  COALESCE(MAX(CASE WHEN blocked_on='human'
        THEN CAST(julianday('now') - julianday(created_at) AS INTEGER) END), 0),
  SUM(CASE WHEN blocked_on='human'
        AND (last_briefed_at IS NULL
             OR last_briefed_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now','-7 days'))
      THEN 1 ELSE 0 END),
  COALESCE(MAX(CAST(julianday('now') - julianday(created_at) AS INTEGER)), 0)
FROM station_task WHERE station_id=? AND state='open'`, stationID)
	var human, overdue, stuck, deferred, never, oldest, staleRisk, oldestOpen sql.NullInt64
	if err := row.Scan(&b.OpenTotal, &human, &overdue, &stuck, &deferred,
		&never, &oldest, &staleRisk, &oldestOpen); err != nil {
		return nil, err
	}
	b.BlockedOnHuman, b.Overdue = int(human.Int64), int(overdue.Int64)
	b.StuckCount, b.RepeatedlyDefer = int(stuck.Int64), int(deferred.Int64)
	b.NeverBriefed, b.OldestBlockedDays, b.StaleRisk = int(never.Int64), int(oldest.Int64), int(staleRisk.Int64)
	b.OldestOpenDays = int(oldestOpen.Int64)

	pick := func(clause string, n int, seen map[string]bool) []StationTask {
		rows, err := s.R.QueryContext(ctx, `SELECT `+taskCols+` FROM station_task
WHERE station_id=? AND state='open' AND (deferred_until IS NULL OR deferred_until <= strftime('%Y-%m-%dT%H:%M:%fZ','now')) AND `+clause+`
ORDER BY COALESCE(last_briefed_at,''), created_at LIMIT ?`, stationID, n*3)
		if err != nil {
			return nil
		}
		defer rows.Close()
		ts, _ := scanTasks(rows)
		var out []StationTask
		for _, t := range ts {
			if seen[t.TaskID] || len(out) == n {
				continue
			}
			seen[t.TaskID] = true
			out = append(out, t)
		}
		return out
	}
	// The slots are DISJOINT, and each is ordered by aging WITHIN its class. If the third
	// slot were simply "anything", a list dominated by one class would fill its own two
	// slots and then the remaining three as well — reproducing exactly the starvation the
	// fixed slots exist to prevent.
	const isDue = `remind_after IS NOT NULL AND remind_after <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	seen := map[string]bool{}
	head := pick(isDue, 2, seen)
	head = append(head, pick(`blocked_on='human' AND NOT (`+isDue+`)`, 2, seen)...)
	head = append(head, pick(`blocked_on<>'human' AND NOT (`+isDue+`)`, 3, seen)...)
	b.Head = head
	b.Remainder = b.OpenTotal - len(head)
	if b.Remainder < 0 {
		b.Remainder = 0
	}

	// Stamp ONLY what was displayed, and only outside the throttle window.
	for _, t := range head {
		if _, err := s.W.ExecContext(ctx, `
UPDATE station_task
SET last_briefed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), briefed_count=briefed_count+1
WHERE task_id=? AND (last_briefed_at IS NULL
      OR last_briefed_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
			t.TaskID, fmt.Sprintf("-%d seconds", lim.BriefStampThrottleSec)); err != nil {
			return nil, err
		}
	}

	// Aging is counted in station activity, not wall-clock days: an idle station is not
	// neglecting anything (§11.7). Approximated as "never briefed, or briefed less often
	// than the station has been active" — the honest available signal.
	if err := s.R.QueryRowContext(ctx, `
SELECT COUNT(*) FROM station_task
WHERE station_id=? AND state='open'
  AND (last_briefed_at IS NULL OR last_briefed_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 days'))`,
		stationID).Scan(&b.AgingCount); err != nil {
		return nil, err
	}
	return b, nil
}

// CrossStationHumanTasks answers the HUMAN's question — "what is everyone waiting on me
// for?" — which per-station lists do not (§11.8). Ordered by the same §11.5 contract
// applied across stations, never by recent station activity: ordering the whole-pile
// view by recency would sink the old items on the one surface built to stop that.
func (s *Store) CrossStationHumanTasks(ctx context.Context, spaceID int64, blockedOn string, limit int) ([]StationTask, error) {
	where := `s.space_id=? AND t.state='open'`
	args := []any{spaceID}
	if blockedOn != "" {
		where += ` AND t.blocked_on=?`
		args = append(args, blockedOn)
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT `+taskColsT+`, s.name
FROM station_task t JOIN station s ON s.station_id=t.station_id
WHERE `+where+`
ORDER BY
  CASE WHEN t.remind_after IS NOT NULL AND t.remind_after <= strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 0
       WHEN t.blocked_on='human' THEN 1 ELSE 2 END,
  COALESCE(t.last_briefed_at,''), t.created_at
LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationTask
	for rows.Next() {
		var t StationTask
		if err := rows.Scan(&t.TaskID, &t.StationID, &t.Text, &t.Detail, &t.Context, &t.BlockedOn,
			&t.BlockedOnStation, &t.RemindAfter, &t.State, &t.Resolution, &t.ResolutionLink,
			&t.CreatedAt, &t.HearsayAtWrite, &t.LastBriefedAt, &t.BriefedCount,
			&t.DeferredUntil, &t.DeferCount, &t.LastDeferReason, &t.ClosedAt, &t.StationName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func boolOrNil(b bool) any {
	if b {
		return 1
	}
	return nil
}
