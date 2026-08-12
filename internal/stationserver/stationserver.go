package stationserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
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

// StationStaffing is what COMM knows about who is actually at a station. Declared
// here rather than imported so the dependency runs one way: the wiring in cmd/ken
// adapts comm's own type into this one.
type StationStaffing struct {
	Endpoints  int
	LastSeenAt string
}

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
	// Staffing reports, per station id, how many live COMM endpoints are reading for
	// it and when the freshest was last seen. Optional, and shaped as a hook for the
	// same reason Hearsay is: this package must not depend on COMM. With COMM off it
	// is nil and the directory OMITS reachability entirely rather than reporting
	// every station as unstaffed — "unknown" and "nobody is there" are different
	// facts, and a directory that conflates them is worse than one that says less.
	Staffing func(ctx context.Context) (map[string]StationStaffing, error)

	// Limits bound the assets. Every one is a BACKUP decision (S12). These are the
	// STARTING values; SetLimits replaces them live.
	TaskLimits   store.StationTaskLimits
	NoteLimits   store.StationNoteLimits
	LockerLimits store.StationLockerLimits
	VaultLimits  store.StationVaultLimits

	// limits reads the live bundle. Set by NewHTTPHandler; nil in tests that build a
	// server directly, where the starting values stand.
	limits func() *limits
}

// taskLim, noteLim and lockerLim read the live bounds, falling back to whatever the
// Deps were built with when no live source is wired (tests).
func (d Deps) taskLim() store.StationTaskLimits {
	if d.limits != nil {
		return d.limits().Task
	}
	return d.TaskLimits
}
func (d Deps) noteLim() store.StationNoteLimits {
	if d.limits != nil {
		return d.limits().Note
	}
	return d.NoteLimits
}
func (d Deps) lockerLim() store.StationLockerLimits {
	if d.limits != nil {
		return d.limits().Locker
	}
	return d.LockerLimits
}
func (d Deps) vaultLim() store.StationVaultLimits {
	if d.limits != nil {
		return d.limits().Vault
	}
	return d.VaultLimits
}

// Handler is the station MCP endpoint.
type Handler struct {
	http.Handler
	// lim is swapped whole on a settings change and read per call, so an operator's
	// edit applies live rather than at the next restart — the point of a live
	// settings page. Every one of these bounds is a BACKUP decision (S12), which is
	// the case where waiting for a restart is least acceptable: an operator lowering
	// a cap is usually reacting to something already growing.
	lim atomic.Pointer[limits]
}

// limits is the swappable bundle.
type limits struct {
	Task   store.StationTaskLimits
	Note   store.StationNoteLimits
	Locker store.StationLockerLimits
	Vault  store.StationVaultLimits
}

// SetLimits applies new bounds live. Zero-valued members keep their defaults, so a
// partially-filled struct cannot silently set a cap to zero — which for a retention
// bound would mean "prune everything".
func (h *Handler) SetLimits(task store.StationTaskLimits, note store.StationNoteLimits, locker store.StationLockerLimits, vault store.StationVaultLimits) {
	if task.MaxOpen == 0 {
		task = store.DefaultStationTaskLimits()
	}
	if note.MaxPageBytes == 0 {
		note = store.DefaultStationNoteLimits()
	}
	if locker.MaxBlobBytes == 0 {
		locker = store.DefaultStationLockerLimits()
	}
	if vault.MaxSecretBytes == 0 {
		vault = store.DefaultStationVaultLimits()
	}
	h.lim.Store(&limits{Task: task, Note: note, Locker: locker, Vault: vault})
}

// NewHTTPHandler builds the endpoint: a streamable-HTTP MCP server wrapped in
// station-only bearer auth.
// sessionTimeout closes an MCP session that has gone quiet. A working conversation
// calls far more often than this; a session left open by a closed laptop or a crashed
// client is gone within the half hour rather than never. It is a backstop, not an
// authorization control — the middleware re-authenticates every HTTP request.
const sessionTimeout = 30 * time.Minute

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
	if d.VaultLimits.MaxSecretBytes == 0 {
		d.VaultLimits = store.DefaultStationVaultLimits()
	}
	h := &Handler{}
	h.SetLimits(d.taskLim(), d.noteLim(), d.lockerLim(), d.vaultLim())
	d.limits = func() *limits { return h.lim.Load() }
	srv := newServer(d)
	// Idle sessions are closed. The SDK's zero value means "never", which is what
	// passing nil asked for: a connection opened once stayed open, and authorized from
	// the handler's point of view, for as long as the client held it.
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})
	h.Handler = authMiddleware(d.Store, d.TokenLimiter, d.Metrics, inner)
	return h
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
- Keep the 'handoff' page current as you go — with mode='replace' and if_rev, NOT append.
  A handoff written only when you know you are leaving is never written, because sessions
  rarely get notice. But append stores a full copy of the page as history EVERY time, so
  history grows with the square of the page: one measured station reached 96% of its cap
  with 252,759 bytes of history behind an 8,083-byte head, while a LARGER page maintained
  by replace cost a tenth of that.
- The routing rule: would a session on a DIFFERENT station, months from now, want this?
  Then it is kb_save / kb_propose_enhancement in the knowledge base, not a notebook page.
  The notebook is for what only this post needs, only for now.
- station_note_promote does not write knowledge; it asks your human to convert a page.

LOCKER — the non-secret half of a working identity: memory and instruction files, tool
preferences, conventions. NEVER a token, key or password — those go in the VAULT, which
exists for exactly that. Ken cannot inspect a blob and know it is a secret, so this rule
is yours to keep.

VAULT — where a credential belongs: station_vault_put / _get / _list / _delete. Three
things about it are worth knowing rather than discovering:
- Values are stored UNENCRYPTED and travel in every backup. The protection is the machine
  and the backup, not Ken. This is a deliberate decision, not a gap — a key kept beside
  the ciphertext protects nobody who can read the file.
- Every READ is logged and shows up in your human's console with the name and the time.
- Nothing is destroyed. An overwrite keeps the previous value and a delete is reversible,
  so a mistake here costs your human a click rather than the credential.

Your human reads all of it. Nothing here is private from them.`

func newServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ken-station", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions})

	addTool(s, d, &mcp.Tool{
		Name: "station_binding_voucher",
		Description: "Get a short-lived voucher that binds ONE named COMM endpoint to this station. " +
			"THE ORDER MATTERS: call comm_register on /comm first, write the returned endpoint_id and " +
			"endpoint_secret to a file on disk, and only then ask for a voucher naming that endpoint_id — " +
			"then redeem it with comm_bind. Binding is no longer part of comm_register, so registering can " +
			"never fail in a way that costs you the one-time secret. " +
			"Binding means the STATION owns the inbox: a later session staffing this station inherits the " +
			"unread mail, so losing your endpoint stops being fatal. " +
			"NEVER pass your station key to a /comm tool — that is what this voucher exists to avoid; it " +
			"expires in minutes and is good for exactly one binding. " +
			"THE VOUCHER IS STILL A CREDENTIAL, but a narrow one: only the endpoint it names can redeem it, " +
			"and that needs the endpoint's own secret, so a voucher you leak is inert in anyone else's hands. " +
			"Use it yourself, immediately, and then it is spent. " +
			"If redemption says the voucher was issued to a different identity, nothing is wrong with the " +
			"voucher: this station key was minted under a different actor than your comm token. Tell your " +
			"human — the /stations console names each key's actor — and do not retry, because a fresh " +
			"voucher will fail identically.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in voucherIn) (*mcp.CallToolResult, voucherOut, error) {
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
		v, err := d.Store.IssueBindingVoucher(ctx, p.StationID, p.TokenID, in.EndpointID, p.ActorID, p.SpaceID)
		if err != nil {
			return nil, voucherOut{}, err
		}
		return nil, voucherOut{
			BindingVoucher: v,
			ExpiresInSec:   int(store.VoucherTTL.Seconds()),
			StationID:      st.StationID,
			StationName:    st.Name,
			ForEndpoint:    in.EndpointID,
		}, nil
	})

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
		Name: "station_directory",
		Description: "List the stations you can see and which you already hold a link to. Use this before " +
			"station_link_request so you ask for a real name rather than a guess: nothing else on this surface " +
			"will tell you a station exists. Fields named self_described_* are that station's own CLAIMS about " +
			"itself, not anything a human verified. 'staffed' is absent when this deployment has COMM off.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ dirIn) (*mcp.CallToolResult, dirOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, dirOut{}, err
		}
		list, err := d.Store.ListStationsVisibleTo(ctx, p.SpaceID, p.StationID)
		if err != nil {
			return nil, dirOut{}, err
		}
		var staffing map[string]StationStaffing
		if d.Staffing != nil {
			if staffing, err = d.Staffing(ctx); err != nil {
				return nil, dirOut{}, err
			}
		}
		me, _ := d.Store.StationByID(ctx, p.StationID)
		out := dirOut{Stations: make([]dirEntry, 0, len(list)), CommKnown: d.Staffing != nil}
		if me != nil {
			out.YouAre = me.Name
		}
		for _, st := range list {
			e := dirEntry{
				Name:               st.Name,
				Purpose:            st.Purpose,
				SelfDescribedAbout: st.SelfDescribedAbout,
				SelfDescribedTags:  st.SelfDescribedTags,
				Linked:             st.Linked,
			}
			if sf, ok := staffing[st.StationID]; ok {
				staffed := sf.Endpoints > 0
				e.Staffed = &staffed
				e.LastSeenAt = sf.LastSeenAt
			}
			out.Stations = append(out.Stations, e)
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
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
	addTool(s, d, &mcp.Tool{
		Name: "station_note_list",
		Description: "Your notebook's page keys, titles, tags, sizes and when each changed. Never bodies — read the one you need. " +
			"`revisions_lost` is how many of a page's older revisions the history bound has ALREADY deleted, oldest first: " +
			"nothing warns you when that happens, so this is the only place it shows. `history_bytes` grows with the SQUARE " +
			"of a page kept by append, which is why it reaches the cap long before the page looks large. " +
			"Working state only: durable lessons belong in the knowledge base.",
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
				Rev: n.Rev, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt,
				RevisionsLost: n.RevisionsLost, HistoryBytes: n.HistoryBytes})
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
		Name: "station_note_write",
		Description: "Append to or replace a notebook page. Keep the 'handoff' page current AS YOU GO, rewriting it with mode='replace' and if_rev rather than appending — append keeps a full copy of the page per revision, so history grows with the SQUARE of its length and silently prunes your oldest revisions — a handoff " +
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
		n, err := d.Store.WriteStationNote(ctx, d.noteLim(), p.StationID, in.Key, in.Title, in.Body,
			in.Tags, mode, in.IfRev, p.TokenID, p.ActorID, hearsayFor(ctx, d, p))
		if err != nil {
			return nil, noteOut{}, err
		}
		return nil, noteOut{Key: n.Key, Title: n.Title, Tags: n.Tags, Body: n.Body,
			Rev: n.Rev, Bytes: n.Bytes, UpdatedAt: n.UpdatedAt}, nil
	})

	addTool(s, d, &mcp.Tool{
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
	addTool(s, d, &mcp.Tool{
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
		t, near, err := d.Store.AddStationTask(ctx, d.taskLim(), store.StationTask{
			StationID: p.StationID, Text: in.Text, Detail: in.Detail, Context: in.Context,
			BlockedOn: in.BlockedOn, RemindAfter: in.RemindAfter,
		}, p.TokenID, p.ActorID, hearsayFor(ctx, d, p))
		if err != nil {
			return nil, taskAddOut{}, err
		}
		return nil, taskAddOut{Task: taskViewOf(*t), NearMatches: taskViews(near)}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_task_list",
		Description: "Your tasks, ordered: overdue first, then what is blocked on your human, then whatever has " +
			"gone longest without being raised. A pure query — reading it does not count as raising anything.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, taskListOut, error) {
		p, err := requireStation(ctx)
		if err != nil {
			return nil, taskListOut{}, err
		}
		ts, total, err := d.Store.ListStationTasks(ctx, d.taskLim(), p.StationID, in.State, in.BlockedOn, in.Limit)
		if err != nil {
			return nil, taskListOut{}, err
		}
		return nil, taskListOut{Tasks: taskViews(ts), Total: total, Shown: len(ts)}, nil
	})

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
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
	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
		Name: "station_locker_put",
		Description: "Store a small text file against this station. NEVER put a token, key or password here — Ken " +
			"cannot tell, your human can read it, and it goes into every backup.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lockerPutIn) (*mcp.CallToolResult, lockerMeta, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, lockerMeta{}, err
		}
		e, err := d.Store.PutStationLockerBlob(ctx, d.lockerLim(), p.StationID, in.Name,
			[]byte(in.Body), in.ContentType, p.TokenID, p.ActorID)
		if err != nil {
			return nil, lockerMeta{}, err
		}
		return nil, lockerMeta{Name: e.Name, Bytes: e.SizeBytes, SHA256: e.SHA256, ContentType: e.ContentType}, nil
	})

	addTool(s, d, &mcp.Tool{
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

	addTool(s, d, &mcp.Tool{
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

	// --- vault --------------------------------------------------------------
	//
	// The locker's sibling, and the answer to what its own text forbids. Gated on the
	// same station scope for the same reason: a station either has a vault or it does
	// not, and "does this key carry one" is not a question a session can act on.
	addTool(s, d, &mcp.Tool{
		Name: "station_vault_list",
		Description: "Secrets held for this station — names, notes, digests and how often each has been read. " +
			"NEVER values: reading one is a separate, logged call. Entries marked with deleted_at are recoverable.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, vaultListOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, vaultListOut{}, err
		}
		es, err := d.Store.ListStationVault(ctx, p.StationID)
		if err != nil {
			return nil, vaultListOut{}, err
		}
		out := vaultListOut{Secrets: make([]vaultMeta, 0, len(es))}
		for _, e := range es {
			out.Secrets = append(out.Secrets, vaultMeta{Name: e.Name, Note: e.Note, Bytes: e.SizeBytes,
				SHA256: e.SHA256, Rev: e.Rev, ReadCount: e.ReadCount, UpdatedAt: e.UpdatedAt, DeletedAt: e.DeletedAt})
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_vault_put",
		Description: "Store a credential against this station — a token, key, password or connection string. This is " +
			"where those belong; the LOCKER is not. Two things to know rather than discover: your human can read " +
			"anything here from the console, and it is stored unencrypted, so the protection is the machine and the " +
			"backup rather than Ken. Writes are reversible — an overwrite keeps the previous value.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vaultPutIn) (*mcp.CallToolResult, vaultPutOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, vaultPutOut{}, err
		}
		e, dropped, err := d.Store.PutStationVaultSecret(ctx, d.vaultLim(), p.StationID, in.Name, in.Secret, in.Note, p.TokenID, p.ActorID)
		if err != nil {
			return nil, vaultPutOut{}, err
		}
		return nil, vaultPutOut{
			vaultMeta:      vaultMeta{Name: e.Name, Note: e.Note, Bytes: e.SizeBytes, SHA256: e.SHA256, Rev: e.Rev},
			HistoryDropped: dropped,
		}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_vault_get",
		Description: "Read one secret back. THIS CALL IS LOGGED — the name, the time and your identity appear in your " +
			"human's console, which is the point of keeping credentials here rather than in a file. Use the value; " +
			"do not repeat it into the conversation unless you have to.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vaultGetIn) (*mcp.CallToolResult, vaultGetOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, vaultGetOut{}, err
		}
		e, err := d.Store.GetStationVaultSecret(ctx, d.vaultLim(), p.StationID, in.Name, "station", p.TokenID, p.ActorID)
		if err != nil {
			return nil, vaultGetOut{}, err
		}
		return nil, vaultGetOut{Name: e.Name, Secret: e.Secret, Note: e.Note,
			Bytes: e.SizeBytes, SHA256: e.SHA256, ReadCount: e.ReadCount}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_vault_delete",
		Description: "Retire a secret. It stops being readable immediately and stays RECOVERABLE from your human's " +
			"console — deliberately unlike station_locker_delete, which destroys. Rotating a credential is a put, not " +
			"a delete then a put.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vaultGetIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireLocker(ctx)
		if err != nil {
			return nil, okOut{}, err
		}
		err = d.Store.DeleteStationVaultSecret(ctx, d.vaultLim(), p.StationID, in.Name, p.TokenID, p.ActorID)
		return nil, okOut{OK: err == nil}, err
	})

	return s
}

// requireLocker gates the locker on the STATION scope alone. The locker is part of
// what a station IS, not an extra a key may be denied.
//
// It shipped as a separate withholdable scope, on the reasoning that a key which only
// keeps notes and tasks should not also carry files. In practice that produced a
// station whose capabilities depended on which key a session happened to be handed —
// so "does this station have a locker" had no answer, only "does this key". A session
// discovering the locker missing cannot tell an intentionally restricted key from a
// misconfigured one, and the locker is exactly where a fresh session on a new machine
// finds what it needs to reconstitute itself.
//
// ScopeStationLocker is NOT removed from the vocabulary: existing keys carry it, and
// COMPATIBILITY.md reserves `station` and `station-locker` together precisely so they
// can be merged later — "splitting a shipped scope is a MAJOR, merging two is free".
// This is that merge. The constant stays so an old key's scope list still parses and
// so nothing has to migrate.
func requireLocker(ctx context.Context) (*principal, error) {
	return requireStation(ctx)
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
	b, err := d.Store.BriefStationTasks(ctx, d.taskLim(), p.StationID)
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
		out.Handoff = "no handoff page yet — write one as you go, not on the way out, and keep it with mode='replace' rather than append"
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

// addTool registers a tool and wraps it with the caller's identity and timing.
//
// THE IDENTITY WRAP IS WHY A HANDLER KNOWS WHO IS CALLING IT. The go-sdk binds a
// session to the INITIALIZE request's context (`server.Connect(req.Context(), …)` in
// mcp/streamable.go), so requireStation and principalFrom read a principal fixed when
// the connection opened, not the one that authenticated this call. The middleware
// authenticates every HTTP request and puts a principal in that request's context; the
// handler never sees it, because the handler runs on the connection's context.
//
// Demonstrated on the knowledge-base surface before being fixed there: a kb_save
// presented with token B, on a session opened by token A, was written with A as
// author_actor_id. Stations carry the same defect for a different reason — a station
// key is per station, so a stale principal means a handler acting on the wrong
// STATION's notebook, tasks and locker.
//
// req.Extra.Header is the only per-call channel the SDK offers, so the principal is
// re-derived once per tool call and every existing requireStation call site keeps
// working while finally meaning what it always claimed.
func addTool[In, Out any](s *mcp.Server, d Deps, t *mcp.Tool,
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	name := t.Name
	reg := d.Metrics
	handler := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		ctx = withCaller(ctx, d.Store, req)
		if reg == nil {
			return h(ctx, req, in)
		}
		start := time.Now()
		res, out, err := h(ctx, req, in)
		reg.RecordMCP(name, err == nil)
		// Nothing on this endpoint blocks, so every tool's duration is real work
		// time and all of them are bucketed — unlike comm_poll, which parks.
		reg.RecordMCPDuration(name, time.Since(start))
		return res, out, err
	}
	mcp.AddTool(s, t, handler)
}

// withCaller replaces the connection-time principal with the one presented on THIS
// call, when the transport can tell us.
//
// Falls back to the context principal when there is no per-call evidence: an
// in-process transport with no HTTP behind it, a request with no bearer, or a key that
// no longer authenticates. That cannot admit anyone — the middleware has already
// rejected unauthenticated requests before the SDK sees them, so arriving here means a
// valid station key was presented on this request.
func withCaller(ctx context.Context, st *store.Store, req *mcp.CallToolRequest) context.Context {
	if st == nil || req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ctx
	}
	tok := bearerFromHeader(req.Extra.Header)
	if tok == "" {
		return ctx
	}
	sp, err := st.AuthenticateStationKey(ctx, tok)
	if err != nil || sp == nil || !hasScope(sp.Scopes, ScopeStation) {
		return ctx
	}
	scopes := map[string]bool{}
	for _, s := range sp.Scopes {
		scopes[s] = true
	}
	return context.WithValue(ctx, ctxKey{}, &principal{
		ActorID: sp.ActorID, TokenID: sp.TokenID, SpaceID: 1,
		StationID: sp.StationID, Scopes: scopes,
	})
}
