package stationserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// The /station MCP endpoint (docs/STATIONS.md §6).
//
// Everything here works with COMM OFF (S2): the notebook and the task list are valuable
// to a solo session with no peers, and gating them behind a messaging feature they have
// nothing to do with would be the wrong dependency. Peer links and endpoint binding are
// the COMM half and live elsewhere.

// Deps are the collaborators for the station endpoint.
type Deps struct {
	// Store is the knowledge base: it holds stations, their assets, and the keys this
	// endpoint authenticates. Unlike COMM's Deps.Store, this one is read AND written —
	// but only station tables. Nothing here writes a curated row.
	Store *store.Store
	// TokenLimiter is this endpoint's own rate bucket. Optional.
	TokenLimiter ratelimit.Limiter
	// Metrics is optional.
	Metrics *metrics.Registry
	// Hearsay reports whether an actor recently RECEIVED inter-session traffic, so a
	// note or task written mid-conversation is marked (§7). Optional: with COMM off it
	// is nil and the marking is simply absent — "no signal", never "known clean".
	Hearsay func(ctx context.Context, actorID int64) bool
	// Limits bound the assets. Every one is a BACKUP decision (S12).
	TaskLimits   store.StationTaskLimits
	NoteLimits   store.StationNoteLimits
	LockerLimits store.StationLockerLimits
}

// Handler is the station MCP endpoint.
type Handler struct{ http.Handler }

// NewHTTPHandler builds the endpoint: a streamable-HTTP MCP server wrapped in
// station-only bearer auth.
func NewHTTPHandler(d Deps) *Handler {
	if d.TaskLimits.MaxOpen == 0 {
		d.TaskLimits = store.DefaultStationTaskLimits()
	}
	if d.NoteLimits.MaxPageBytes == 0 {
		d.NoteLimits = store.DefaultStationNoteLimits()
	}
	if d.LockerLimits.MaxBlobBytes == 0 {
		d.LockerLimits = store.DefaultStationLockerLimits()
	}
	srv := newServer(d)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return &Handler{Handler: authMiddleware(d.Store, d.TokenLimiter, d.Metrics, inner)}
}

// instructions are delivered on connect. The FOURTH sentence about tasks is the one that
// makes the feature work rather than merely exist: a briefing the model reads and does
// not relay is the original failure with extra steps (§11.9).
//
// This text is not a control — Ken cannot verify that a model captured an item or told
// the human about one (§11.2). It changes behaviour in the common case, which is worth
// having, and the design says plainly where it stops.
const instructions = `Ken stations — a durable working identity you STAFF (docs/STATIONS.md).

You are staffing a station: a post that outlives this conversation. It owns a notebook,
a task list and a small locker, and they are all still here next time.

START HERE, EVERY SESSION:
- Call station_me first. It returns your briefing: open tasks (named, not just counted),
  how stale your handoff page is, and what is waiting on your human.
- IN YOUR FIRST MESSAGE, TELL YOUR HUMAN IN WORDS every task blocked on them and
  everything past its date. The briefing is a tool result; it reaches nobody unless you
  say it. This is the point of the whole feature.

TASKS — the list exists because pending things decay out of a conversation:
- Add the moment you say "we should" — station_task_add is one line and one call. Adding
  late means not adding.
- blocked_on is required: self = you can act now; human = it cannot move until your human
  does or decides; peer = another station owes something.
- CLOSE the moment a thing is done, not at the end of the session. Closing takes several
  ids at once.
- If an item has been briefed repeatedly and nothing changed, say what is blocking it or
  defer it with a reason. Do NOT silently leave it, and do NOT drop something your human
  owes — that is theirs to abandon, not yours.

NOTEBOOK — working state, not knowledge:
- Keep the 'handoff' page current as you go. A handoff written only when you know you are
  leaving is never written, because sessions rarely get notice.
- The routing rule: would a session on a DIFFERENT station, months from now, want this?
  Then it is kb_save / kb_propose_enhancement in the knowledge base, not a notebook page.
  The notebook is for what only this post needs, only for now.
- station_note_promote does not write knowledge; it asks your human to convert a page.

LOCKER — the non-secret half of a working identity: memory and instruction files, tool
preferences, conventions. NEVER a token, key or password. Ken cannot inspect a blob and
know it is a secret, so this rule is yours to keep.

Your human reads all of it. Nothing here is private from them.`

func newServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ken-station", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_binding_voucher",
		Description: "Get a short-lived voucher that binds a COMM endpoint to this station. Pass it to " +
			"comm_register as binding_voucher on the /comm endpoint, THEN write the returned endpoint_id and " +
			"endpoint_secret to a file on disk. Binding means the STATION owns the inbox: a later session " +
			"staffing this station inherits the unread mail, so losing your endpoint stops being fatal. " +
			"NEVER pass your station key to comm_register — that is what this voucher exists to avoid; it " +
			"expires in minutes and is good for exactly one binding.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ voucherIn) (*mcp.CallToolResult, voucherOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, voucherOut{}, err
		}
		st, err := d.Store.StationByID(ctx, p.StationID)
		if err != nil {
			return nil, voucherOut{}, err
		}
		// Refuse at ISSUE for an archived station, rather than handing out a voucher
		// that redemption will always reject. An archived station's keys stop binding
		// (S3), so the voucher could never work — and comm_register would report it
		// as "unknown, already used, or expired", which is three wrong reasons that
		// send the session looking for a problem it does not have.
		if st.State != "active" {
			return nil, voucherOut{}, fmt.Errorf("station %q is archived, so it cannot bind new endpoints — tell your human; they can unarchive it from the /stations console", st.Name)
		}
		// p.TokenID, not anything the caller supplied: the voucher records which KEY
		// asked, so revoking that key later severs the endpoints it bound (S6).
		v, err := d.Store.IssueBindingVoucher(ctx, p.StationID, p.TokenID)
		if err != nil {
			return nil, voucherOut{}, err
		}
		return nil, voucherOut{
			BindingVoucher: v,
			ExpiresInSec:   int(store.VoucherTTL.Seconds()),
			StationID:      st.StationID,
			StationName:    st.Name,
		}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_link_request",
		Description: "Ask your human to let this station talk to another one. An approved link is a standing " +
			"relationship: either side can then open a channel when it needs one, with no pairing code. The reason " +
			"you give is shown to YOUR HUMAN only and is never delivered to the other station, so write it for the " +
			"person deciding, not for the peer. You will be told it is pending either way — do not re-ask in a loop.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in linkRequestIn) (*mcp.CallToolResult, linkRequestOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, linkRequestOut{}, err
		}
		if strings.TrimSpace(in.Reason) == "" {
			return nil, linkRequestOut{}, errors.New("a reason is required — your human decides on it, and an unexplained request is one they cannot judge")
		}
		target, err := d.Store.StationByName(ctx, p.SpaceID, strings.TrimSpace(in.ToStation))
		if err != nil {
			return nil, linkRequestOut{}, errors.New("no such station is available to link to — ask your human for the exact name")
		}
		// The hearsay marker, resolved the same way a note or task resolves it. This
		// is the ONLY signal the human gets that the request may not be this session's
		// own idea: a peer cannot open a channel, but it can talk this session into
		// asking for one, and the request then arrives looking like its own.
		if _, err := d.Store.CreateStationLinkRequest(ctx, p.SpaceID, p.TokenID,
			p.StationID, target.StationID, in.Reason, hearsayFor(ctx, d, p)); err != nil {
			return nil, linkRequestOut{}, err
		}
		// Identical answer whether the request was filed or silently dropped against a
		// muted pair. A caller that could tell the difference could probe the human's
		// past refusals one request at a time.
		return nil, linkRequestOut{
			Status: "pending",
			Note:   "Submitted for your human to decide. Tell them in words that you asked and why — this tool result reaches nobody otherwise. Do not ask again for this station.",
		}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_me",
		Description: "Your briefing: who you are staffing, open tasks (named), handoff staleness, and what is " +
			"waiting on your human. Call it FIRST in every session, and relay what it says to your human in words — " +
			"it is a tool result and reaches nobody otherwise. Optionally updates how you describe yourself.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in meIn) (*mcp.CallToolResult, meOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, meOut{}, err
		}
		if in.SelfDescribedAbout != "" || in.SelfDescribedTags != nil {
			if err := d.Store.SetStationSelfDescription(ctx, p.StationID, in.SelfDescribedAbout, in.SelfDescribedTags); err != nil {
				return nil, meOut{}, err
			}
		}
		out, err := buildBriefing(ctx, d, p)
		return nil, out, err
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_request",
		Description: "Ask your human to create a station for you. You supply a PURPOSE and may suggest a name; " +
			"your human types the real name when they approve. This is the only tool a key with no station may call.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in requestIn) (*mcp.CallToolResult, requestOut, error) {
		p := principalFrom(ctx)
		if p == nil {
			return nil, requestOut{}, errors.New("unauthenticated")
		}
		if strings.TrimSpace(in.Purpose) == "" {
			return nil, requestOut{}, errors.New("say what the station is for — your human approves on the purpose, not the name")
		}
		id, err := d.Store.CreateStationRequest(ctx, p.SpaceID, p.TokenID, p.StationID, in.NameHint, in.Purpose)
		if err != nil {
			return nil, requestOut{}, err
		}
		return nil, requestOut{RequestID: id, State: "pending_human_approval",
			Guidance: "A human must approve this and will type the name themselves. Nothing happens until they do."}, nil
	})

	// --- notebook -----------------------------------------------------------
	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_note_list",
		Description: "Your notebook's page keys, titles, tags, sizes and when each changed. Never bodies — " +
			"read the one you need. Working state only: durable lessons belong in the knowledge base.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, noteListOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, noteListOut{}, err
		}
		ns, err := d.Store.ListStationNotes(ctx, p.StationID)
		if err != nil {
			return nil, noteListOut{}, err
		}
		out := noteListOut{Pages: make([]noteMeta, 0, len(ns))}
		for _, n := range ns {
			out.Pages = append(out.Pages, noteMeta{Key: n.Key, Title: n.Title, Tags: n.Tags,
				Rev: n.Rev, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt})
		}
		return nil, out, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name:        "station_note_read",
		Description: "Read one notebook page.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in noteReadIn) (*mcp.CallToolResult, noteOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, noteOut{}, err
		}
		n, err := d.Store.ReadStationNote(ctx, p.StationID, in.Key)
		if err != nil {
			return nil, noteOut{}, err
		}
		return nil, noteOut{Key: n.Key, Title: n.Title, Tags: n.Tags, Body: n.Body,
			Rev: n.Rev, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_note_write",
		Description: "Append to or replace a notebook page. Keep the 'handoff' page current AS YOU GO — a handoff " +
			"written only when you know you are leaving is never written. Pass if_rev when you have read the page and " +
			"are overwriting it, so a second session staffing this station cannot be silently clobbered. " +
			"NEVER put a token, key or password here — Ken cannot tell, your human can read it, and it goes into " +
			"every backup. If you are worried about losing a credential when your context is compacted, a page here " +
			"is the WRONG place: reading it needs the station key, so it is unreachable in exactly that emergency. " +
			"Write the RECOVERY PATH instead — which station, which peers, what to re-run. " +
			"Routing rule: if a session on a DIFFERENT station would want this months from now, it is knowledge (kb_save), not a note.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in noteWriteIn) (*mcp.CallToolResult, noteOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, noteOut{}, err
		}
		mode := in.Mode
		if mode == "" {
			mode = "append"
		}
		n, err := d.Store.WriteStationNote(ctx, d.NoteLimits, p.StationID, in.Key, in.Title, in.Body,
			in.Tags, mode, in.IfRev, p.TokenID, p.ActorID, hearsayFor(ctx, d, p))
		if err != nil {
			return nil, noteOut{}, err
		}
		return nil, noteOut{Key: n.Key, Title: n.Title, Tags: n.Tags, Body: n.Body,
			Rev: n.Rev, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_note_promote",
		Description: "Ask your human to turn a notebook page into a knowledge-base entry. This does NOT write " +
			"knowledge: it queues the page for them to review and convert. Use it when a working note has proven " +
			"durable enough that another station would want it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in noteReadIn) (*mcp.CallToolResult, promoteOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, promoteOut{}, err
		}
		id, err := d.Store.PromoteStationNote(ctx, p.StationID, in.Key)
		if err != nil {
			return nil, promoteOut{}, err
		}
		return nil, promoteOut{PromotionID: id, State: "pending_human_review",
			Guidance: "Queued. Your human converts it; nothing enters the knowledge base until they do."}, nil
	})

	// --- tasks --------------------------------------------------------------
	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_task_add",
		Description: "Record something outstanding. Add the MOMENT you say \"we should\" — adding late means not " +
			"adding. blocked_on is required: self = you can act now; human = it cannot move until your human does or " +
			"decides; peer = another station owes something. The result lists near-matches already on your list, so " +
			"you can close or merge instead of duplicating.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskAddIn) (*mcp.CallToolResult, taskAddOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, taskAddOut{}, err
		}
		t, near, err := d.Store.AddStationTask(ctx, d.TaskLimits, store.StationTask{
			StationID: p.StationID, Text: in.Text, Detail: in.Detail, Context: in.Context,
			BlockedOn: in.BlockedOn, RemindAfter: in.RemindAfter,
		}, p.TokenID, p.ActorID, hearsayFor(ctx, d, p))
		if err != nil {
			return nil, taskAddOut{}, err
		}
		return nil, taskAddOut{Task: taskViewOf(*t), NearMatches: taskViews(near)}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_task_list",
		Description: "Your tasks, ordered: overdue first, then what is blocked on your human, then whatever has " +
			"gone longest without being raised. A pure query — reading it does not count as raising anything.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, taskListOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, taskListOut{}, err
		}
		ts, total, err := d.Store.ListStationTasks(ctx, d.TaskLimits, p.StationID, in.State, in.BlockedOn, in.Limit)
		if err != nil {
			return nil, taskListOut{}, err
		}
		return nil, taskListOut{Tasks: taskViews(ts), Total: total, Shown: len(ts)}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_task_close",
		Description: "Close finished tasks — several at once. Do this the moment a thing is done, not at the end " +
			"of the session. A resolution line is required: the record of what happened is the point.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskCloseIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, countOut{}, err
		}
		n, err := d.Store.CloseStationTasks(ctx, p.StationID, in.TaskIDs, in.Resolution, in.ResolutionLink, p.ActorID)
		return nil, countOut{Count: n}, err
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_task_defer",
		Description: "Push a task out to a date, with a reason. Deliberately more work than closing it — deferring " +
			"is legitimate, deferring silently and repeatedly is how a list rots. The count is shown back to you.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskDeferIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, okOut{}, err
		}
		err = d.Store.DeferStationTask(ctx, p.StationID, in.TaskID, in.Until, in.Reason)
		return nil, okOut{OK: err == nil}, err
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_task_drop",
		Description: "Abandon tasks that are no longer worth doing, with a reason. Refused for anything blocked on " +
			"your human unless they decided it themselves — their commitments are theirs to abandon. If something has " +
			"been raised repeatedly and nothing moved, say what is blocking it instead of dropping it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskDropIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, countOut{}, err
		}
		n, err := d.Store.DropStationTasks(ctx, p.StationID, in.TaskIDs, in.Reason, in.HumanDecided, p.ActorID)
		return nil, countOut{Count: n}, err
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name:        "station_task_reopen",
		Description: "Reopen closed or dropped tasks — a decision to drop is sometimes wrong.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskReopenIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, countOut{}, err
		}
		n, err := d.Store.ReopenStationTasks(ctx, p.StationID, in.TaskIDs, in.Reason)
		return nil, countOut{Count: n}, err
	})

	// --- locker -------------------------------------------------------------
	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_locker_list",
		Description: "Files stored against this station — names, sizes, digests. The locker is for what a fresh " +
			"session on another machine needs to reconstitute you: memory and instruction files, conventions. Never secrets.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, lockerListOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, lockerListOut{}, err
		}
		es, err := d.Store.ListStationLocker(ctx, p.StationID)
		if err != nil {
			return nil, lockerListOut{}, err
		}
		out := lockerListOut{Files: make([]lockerMeta, 0, len(es))}
		for _, e := range es {
			out.Files = append(out.Files, lockerMeta{Name: e.Name, Bytes: e.SizeBytes,
				SHA256: e.SHA256, ContentType: e.ContentType, UpdatedAt: e.UpdatedAt})
		}
		return nil, out, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name: "station_locker_put",
		Description: "Store a small text file against this station. NEVER put a token, key or password here — Ken " +
			"cannot tell, your human can read it, and it goes into every backup.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lockerPutIn) (*mcp.CallToolResult, lockerMeta, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, lockerMeta{}, err
		}
		e, err := d.Store.PutStationLockerBlob(ctx, d.LockerLimits, p.StationID, in.Name,
			[]byte(in.Body), in.ContentType, p.TokenID, p.ActorID)
		if err != nil {
			return nil, lockerMeta{}, err
		}
		return nil, lockerMeta{Name: e.Name, Bytes: e.SizeBytes, SHA256: e.SHA256, ContentType: e.ContentType}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name:        "station_locker_get",
		Description: "Read one file back from the locker.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lockerGetIn) (*mcp.CallToolResult, lockerGetOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, lockerGetOut{}, err
		}
		e, err := d.Store.GetStationLockerBlob(ctx, p.StationID, in.Name)
		if err != nil {
			return nil, lockerGetOut{}, err
		}
		return nil, lockerGetOut{Name: e.Name, Body: string(e.Bytes), Bytes: e.SizeBytes, SHA256: e.SHA256}, nil
	})

	addTool(s, d.Metrics, &mcp.Tool{
		Name:        "station_locker_delete",
		Description: "Remove a file from the locker.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lockerGetIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, okOut{}, err
		}
		err = d.Store.DeleteStationLockerBlob(ctx, p.StationID, in.Name)
		return nil, okOut{OK: err == nil}, err
	})

	return s
}

// requireLocker adds the reserved second scope on top of a station. Separated so the
// locker can be withheld from a key that should only keep notes and tasks.
func requireLocker(ctx context.Context) (*principal, error) {
	p, err := requireStation(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Scopes[ScopeStationLocker] {
		return nil, errors.New("this key lacks the station-locker scope")
	}
	return p, nil
}

// hearsayFor computes the write-time hearsay marking (§7) — whether this session had
// recently received COMM traffic. Keyed on the ACTOR, which is why a station key must be
// minted under the same actor as that machine's comm token: a different actor silently
// defeats the marker, and a marker that fails open without saying so is worse than none.
//
// With COMM off there is nothing to check and the marking is simply absent, which is
// "no signal" rather than "known clean" — the same stance COMM's own provenance takes.
func hearsayFor(ctx context.Context, d Deps, p *principal) bool {
	if d.Hearsay == nil {
		return false
	}
	return d.Hearsay(ctx, p.ActorID)
}

func buildBriefing(ctx context.Context, d Deps, p *principal) (meOut, error) {
	st, err := d.Store.StationByID(ctx, p.StationID)
	if err != nil {
		return meOut{}, err
	}
	b, err := d.Store.BriefStationTasks(ctx, d.TaskLimits, p.StationID)
	if err != nil {
		return meOut{}, err
	}
	writtenAt, activities, err := d.Store.HandoffStaleness(ctx, p.StationID)
	if err != nil {
		return meOut{}, err
	}
	out := meOut{
		StationID: st.StationID, Name: st.Name, NameSource: "human", Purpose: st.Purpose,
		SelfDescribedAbout: st.SelfDescribedAbout, SelfDescribedTags: st.SelfDescribedTags,
		Tasks: briefingView{
			Open: b.OpenTotal, BlockedOnHuman: b.BlockedOnHuman, Overdue: b.Overdue,
			NotBriefedRecently: b.AgingCount, BriefedUnchanged: b.StuckCount,
			DeferredRepeatedly: b.RepeatedlyDefer, Remainder: b.Remainder,
			Head: taskViews(b.Head),
		},
	}
	switch {
	case activities < 0:
		out.Handoff = "no handoff page yet — write one as you go, not on the way out"
	case activities == 0:
		out.Handoff = "handoff current (written " + writtenAt + ")"
	default:
		out.Handoff = fmt.Sprintf("handoff written %s, %d activities ago", writtenAt, activities)
	}
	if b.BlockedOnHuman > 0 || b.Overdue > 0 {
		out.Relay = "Tell your human, in words, in your first message: " +
			fmt.Sprintf("%d task(s) are waiting on them and %d are past their date.", b.BlockedOnHuman, b.Overdue)
	}
	return out, nil
}

func taskViews(ts []store.StationTask) []taskView {
	out := make([]taskView, 0, len(ts))
	for _, t := range ts {
		out = append(out, taskViewOf(t))
	}
	return out
}

// taskViewOf is a named function, and tested, because it is a CROSS-LAYER copy: the
// store can grow a field the view silently drops — exactly how a file descriptor once
// went missing between a completed upload and the receiver's poll in COMM.
func taskViewOf(t store.StationTask) taskView {
	return taskView{
		TaskID: t.TaskID, Text: t.Text, Detail: t.Detail, BlockedOn: t.BlockedOn,
		BlockedOnStation: t.BlockedOnStation, RemindAfter: t.RemindAfter, State: t.State,
		CreatedAt: t.CreatedAt, BriefedCount: t.BriefedCount, DeferCount: t.DeferCount,
		DeferredUntil: t.DeferredUntil, LastBriefedAt: t.LastBriefedAt,
		HearsayAtWrite: t.HearsayAtWrite, StationName: t.StationName,
		Resolution: t.Resolution, ResolutionLink: t.ResolutionLink,
	}
}

func addTool[In, Out any](s *mcp.Server, reg *metrics.Registry, t *mcp.Tool,
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	handler := h
	if reg != nil {
		name := t.Name
		handler = func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			start := time.Now()
			res, out, err := h(ctx, req, in)
			reg.RecordMCP(name, err == nil)
			// Nothing on this endpoint blocks, so every tool's duration is real work
			// time and all of them are bucketed — unlike comm_poll, which parks.
			reg.RecordMCPDuration(name, time.Since(start))
			return res, out, err
		}
	}
	mcp.AddTool(s, t, handler)
}
