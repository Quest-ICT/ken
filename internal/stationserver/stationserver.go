package stationserver

import (
	"context"
	"errors"
	"fmt"
	"github.com/Quest-ICT/ken/internal/version"
	"log"
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
// Everything here works with COMM UNAVAILABLE (S2): the notebook and the task list are valuable
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
	// note or task written mid-conversation is marked (§7). Optional: with COMM unavailable it
	// is nil and the marking is simply absent — "no signal", never "known clean".
	Hearsay func(ctx context.Context, actorID int64) bool
	// Staffing reports, per station id, how many live COMM endpoints are reading for
	// it and when the freshest was last seen. Optional, and shaped as a hook for the
	// same reason Hearsay is: this package must not depend on COMM. With COMM unavailable it
	// is nil and the directory OMITS reachability entirely rather than reporting
	// every station as unstaffed — "unknown" and "nobody is there" are different
	// facts, and a directory that conflates them is worse than one that says less.
	Staffing func(ctx context.Context) (map[string]StationStaffing, error)
	// CommEndpoints reports the PUBLIC comm endpoint ids bound to a station, so the
	// briefing every session is told to call first can say which endpoint is its own.
	//
	// A hook rather than a comm import, for the same reason as Staffing and Hearsay: this
	// package must not depend on COMM. Nil when COMM is unavailable, and the field is then OMITTED
	//
	// "UNAVAILABLE", NOT "OFF", AND THE WORD MATTERS BECAUSE IT PROPAGATED. There is no switch:
	// cmd/ken/main.go says "THERE IS NO SWITCH … both are gone", and the only route to nil is
	// comm.db failing to open, which it logs as "a failure, not a setting". Calling that state
	// "COMM off" put a phantom setting into station_directory's SHIPPED description and into
	// three console strings that told operators to turn it back on — in the one state where
	// /comm is unrouted and 404s. PARKING-LOT #44 tracked it; every site is corrected.
	// rather than reported empty — "COMM is not running here" and "you are bound to no
	// endpoint" are different facts, and a briefing that conflates them sends a session
	// hunting for a credentials problem it does not have.
	CommEndpoints func(ctx context.Context, stationID string) ([]string, error)

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
const instructions = `Ken stations — a durable working identity you STAFF: a post that outlives this conversation.

THREE MCP SURFACES: /station/mcp (station_*, this post), /mcp (kb_*, the knowledge base), /comm/mcp (comm_*, messaging with other AI sessions). Each is a SEPARATE entry your human configures; you hold only the ones they set up, so ask for the others BY NAME.

Your station owns a notebook, a task list, a locker and a vault; all still here next time.

FIRST, EVERY SESSION: call station_me. Then, IN YOUR FIRST MESSAGE, TELL YOUR HUMAN IN WORDS every task blocked on them and everything past its date. The briefing is a tool result; it reaches nobody unless you say it. This is the point of the whole feature.

BEFORE TELLING THEM THEY OWE SOMETHING, CHECK THE UNDERLYING STATE — NOT THE FLAG. blocked_on is set once, at creation, and nothing ever revisits it, so a task already satisfied looks exactly like one still waiting, and both count as waiting on them. One kb_search for a knowledge-base item, one command for a release: far cheaper than telling your human twice that they owe what they finished last week.

TASKS: add the moment you say "we should"; adding late means not adding. blocked_on is required: self = you can act now; human = it cannot move until your human does or decides; peer = another station owes something. CLOSE the moment a thing is done, not at session end.

NOTEBOOK is working state: keep the handoff current AS YOU GO with mode='replace', not append. Would a session on a DIFFERENT station want it months from now? Then it is kb_save, not a note.

Credentials go in the vault: NEVER a token, key or password in a locker or a note.

Your human reads all of it; none of it is private from them.`

// mcpKeepAlive matches the interval on the other MCP surfaces. The measurement behind the 30s,
// and why Server.ReadTimeout does not interact with it, are in internal/mcpserver/server.go.
const mcpKeepAlive = 30 * time.Second

func newServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ken-station", Version: "1"},
		&mcp.ServerOptions{Instructions: version.InstructionStamp() + instructions, KeepAlive: mcpKeepAlive})

	addTool(s, d, &mcp.Tool{
		Name: "station_link_request",
		Description: "Ask your human to let this station talk to another one. An approved link is a standing " +
			"relationship: either side can then open a channel when it needs one, with no pairing code. The reason " +
			"you give is shown to YOUR HUMAN only and is never delivered to the other station, so write it for the " +
			"person deciding, not for the peer. You will be told it is pending either way — do not re-ask in a loop.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkRequestIn) (*mcp.CallToolResult, linkRequestOut, error) {
		p, err := requireStation(ctx, req)
		if err != nil {
			return nil, linkRequestOut{}, err
		}
		if strings.TrimSpace(in.Reason) == "" {
			return nil, linkRequestOut{}, errors.New("a reason is required — your human decides on it, and an unexplained request is one they cannot judge")
		}
		// RESOLVED AMONG WHAT THIS CALLER CAN ALREADY SEE, never by bare name.
		//
		// StationByName's own contract says "For CONSOLE and CLI use only: a name is not an
		// address, and no agent-facing path may route by it (S3)" — and this line called it
		// until 2026-08-19. Because that query filters on name alone, a name that
		// existed produced a filed request and one that did not produced a refusal: an
		// enumeration oracle over every station name in the space, INCLUDING the ones
		// withheld from station_directory. And the filed request was the worse half — a
		// correct guess put an agent-authored ask for an unpublished post in front of its
		// human, which is exactly the unsolicited approach publication exists to prevent.
		//
		// Now a station the caller cannot see is indistinguishable from one that does not
		// exist: both arrive here as an error and both get the single refusal below. The
		// discovery surface is station_directory, gated per asker, which is where finding
		// out that a station exists is supposed to happen.
		target, err := d.Store.StationByNameVisibleTo(ctx, p.StationID, strings.TrimSpace(in.ToStation))
		if err != nil {
			return nil, linkRequestOut{}, errors.New("no such station is available to link to — ask your human for the exact name")
		}
		// The hearsay marker, resolved the same way a note or task resolves it. This
		// is the ONLY signal the human gets that the request may not be this session's
		// own idea: a peer cannot open a channel, but it can talk this session into
		// asking for one, and the request then arrives looking like its own.
		if _, err := d.Store.CreateStationLinkRequest(ctx, p.TokenID,
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
			"it is a tool result and reaches nobody otherwise. Optionally updates how you describe yourself. " +
			"THE HEAD IS A SAMPLE, NOT A SUMMARY: it holds at most seven items. Read `never_briefed` (open tasks " +
			"never shown to anyone), `oldest_blocked_on_human_days`, and `blocked_on_human_and_stale` — that last " +
			"one is the population most likely to be ALREADY DONE, because blocked_on is set at creation and " +
			"nothing revisits it. Check the underlying state before telling your human they still owe something. " +
			"`oldest_open_task_days` ages EVERY open item, including the ones blocked on YOU: a task can be accurate " +
			"and no longer the point, and no other figure here can see that. Ken never defers or closes anything on age — " +
			"it shows you the number and you decide." +
			" 'not_shown' reads as a queue awaiting its turn; 'never_briefed' is how many have never had one — when it is non-zero, read the full list with station_task_list rather than trusting the head. 'briefed_count' only rises, so an item raised every session looks attended to: when the age is large, read the list, look at created_at, and ASK whether each item is still worth doing, or done being worth doing.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in meIn) (*mcp.CallToolResult, meOut, error) {
		// *** NO WORKSPACE? YOU GET ONE. NOTHING TO APPROVE, NOTHING TO WAIT FOR. ***
		//
		// docs/IDENTITY.md §5, decided: "fully working, auto-named, no approval." A session in an
		// unknown folder mints a workspace, works immediately — notebook, tasks, vault, knowledge
		// base, nothing withheld — and says so in its first message.
		//
		// IT HAPPENS HERE BECAUSE station_me IS THE CALL EVERY SESSION IS TOLD TO MAKE FIRST, so
		// the fix lands on the path a session already walks rather than adding a step to it.
		//
		// WHAT THIS REPLACES IS A DEADLOCK, not an inconvenience. station_request's own text said
		// it was "the only tool a key with no station may call" — true, and it concealed the real
		// constraint: a key with no station could call it, but a session with no KEY could not,
		// and that is every session being onboarded. The console could not create a station
		// either (there is no POST /stations), and a console-minted key was issued under the
		// OPERATOR's actor, so it could never bind the session it was minted for. Vlad, at the
		// console and unable to give a session Station: "It is absurd the way it works now."
		//
		// The naming pays for itself later rather than being a setup chore (§5): the link-approval
		// screen reads "ken-public wants to talk to ken-prod", which is the one moment a bad name
		// is actually in front of the human.
		out, err := stationMe(ctx, d, req, in)
		if err != nil {
			return nil, meOut{}, err
		}
		// THE VERSION IS STAMPED AT THIS ONE EXIT AND NOWHERE ELSE, and that is the whole point.
		//
		// It used to be set inside buildBriefing, which is only ONE of the two paths through this
		// tool. The other — claimWorkspace — built its own meOut from scratch and never carried it,
		// so `ken_version` and `ken_version_note` came back EMPTY on the workspace-CREATION call.
		//
		// THAT IS THE WORST PLACE IN THE WHOLE SURFACE FOR IT TO BE MISSING. The field exists so a
		// session can tell its manual is stale, and the session most likely to be holding stale
		// text — and least equipped to suspect it — is a brand-new one calling station_me as its
		// first act. The one call where the version signal matters most was the one call that
		// omitted it. ken-prod-ops found it by calling the tool from both a new and an established
		// workspace and diffing the two results, which is the only way it was ever going to show.
		//
		// Setting the two fields on the second path would have fixed this instance and left the
		// shape intact for the third path. Stamping after the handler returns means a future path
		// cannot omit it without deleting this line, and TestEveryStationMePathCarriesTheVersion
		// fails if anyone does.
		out.KenVersion = version.Version
		out.VersionNote = versionNote
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_directory",
		Description: "List the stations you can see and which you already hold a link to. Use this before " +
			"station_link_request so you ask for a real name rather than a guess: nothing else on this surface " +
			"will tell you a station exists. Fields named self_described_* are that station's own CLAIMS about " +
			"itself, not anything a human verified. 'staffed' is absent when this server's message database is not open — a fault, not a setting; nothing turns COMM off.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ dirIn) (*mcp.CallToolResult, dirOut, error) {
		p, err := requireStation(ctx, req)
		if err != nil {
			return nil, dirOut{}, err
		}
		list, err := d.Store.ListStationsVisibleTo(ctx, p.StationID)
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
		Description: "Ask your human to create a station for you, and WAIT for them to approve it. " +
			"YOU ALMOST CERTAINLY WANT station_me INSTEAD: if you have no workspace, station_me makes you one " +
			"immediately — named after your folder, working from the next call, with nothing to approve and " +
			"nobody to wait for. Use this only when your human has told you they want to name and approve this " +
			"one themselves. You supply a PURPOSE and may suggest a name; they type the real name on approval.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in requestIn) (*mcp.CallToolResult, requestOut, error) {
		p := principalFrom(ctx)
		if p == nil {
			return nil, requestOut{}, errors.New("unauthenticated")
		}
		if strings.TrimSpace(in.Purpose) == "" {
			return nil, requestOut{}, errors.New("say what the station is for — your human approves on the purpose, not the name")
		}
		id, err := d.Store.CreateStationRequest(ctx, p.TokenID, p.StationID, in.NameHint, in.Purpose)
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
			" Working state only: durable lessons belong in the knowledge base.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, noteListOut, error) {
		p, err := requireStation(ctx, req)
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
		Name: "station_note_read",
		Description: "Read one notebook page, or a retained older revision of it with `rev`. " +
			"When station_note_list says a page has lost revisions, `rev` is how you read what survived — " +
			"the lowest readable revision is (revisions_lost + 1), and anything below it is gone for good.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in noteReadIn) (*mcp.CallToolResult, noteOut, error) {
		p, err := requireStation(ctx, req)
		if err != nil {
			return nil, noteOut{}, err
		}
		var n *store.StationNote
		if in.Rev > 0 {
			n, err = d.Store.ReadStationNoteRev(ctx, p.StationID, in.Key, in.Rev)
		} else {
			n, err = d.Store.ReadStationNote(ctx, p.StationID, in.Key)
		}
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
			"Routing rule: if a session on a DIFFERENT station would want this months from now, it is knowledge (kb_save), not a note." +
			" Sessions rarely get notice, which is why AS YOU GO is the only schedule that works. One measured station reached 96% of its history cap with 252,759 bytes of history behind an 8,083-byte head, while a LARGER page maintained by replace cost a tenth of that. The routing rule in full: kb_save or kb_propose_enhancement on /mcp for anything a DIFFERENT station would want months from now; the notebook is for what only this post needs, only for now.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in noteWriteIn) (*mcp.CallToolResult, noteOut, error) {
		p, err := requireStation(ctx, req)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in noteReadIn) (*mcp.CallToolResult, promoteOut, error) {
		p, err := requireStation(ctx, req)
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
			"decides; peer = another station owes something. The result lists near-matches already on your list: if " +
			"one is the same commitment, close the duplicate — normally the row you just added, since the older one " +
			"carries the age the ordering depends on — with a resolution naming the id you kept.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskAddIn) (*mcp.CallToolResult, taskAddOut, error) {
		p, err := requireStation(ctx, req)
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
			"gone longest without being raised. A pure query — reading it does not count as raising anything." +
			" This is the FULL list, and the briefing head is only a sample of it: when station_me reports a non-zero 'never_briefed', read it here — what has never been shown to anyone is what is most likely to be stale. When 'oldest_open_task_days' is large, look at created_at and ask whether an item is still worth doing, or done being worth doing — overtaken is not the same as wrong.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, taskListOut, error) {
		p, err := requireStation(ctx, req)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskCloseIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx, req)
		if err != nil {
			return nil, countOut{}, err
		}
		n, err := d.Store.CloseStationTasks(ctx, p.StationID, in.TaskIDs, in.Resolution, in.ResolutionLink, p.ActorID)
		return nil, countOut{Count: n}, err
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_task_defer",
		Description: "Push a task out to a date, with a reason. Deliberately more work than closing it — deferring " +
			"is legitimate, deferring silently and repeatedly is how a list rots. The count is shown back to you. " +
			"IF AN ITEM HAS BEEN BRIEFED REPEATEDLY AND NOTHING CHANGED, say what is blocking it out loud to your human, or defer it with a reason. " +
			"Never silence, and do NOT drop something your human owes — that is theirs to abandon, not yours. " +
			"WRITE THE REASON AS WHAT YOU CHECKED AND WHEN, not as a feeling: \"checked the release tag 2026-08-25, still unpublished\" tells the next " +
			"session what to re-run, while \"still waiting\" makes it start over. The date is a reminder; the REASON is where a recheck is recorded — " +
			"blocked_on is set once at creation and nothing ever revisits it, so there is nowhere else to put one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskDeferIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireStation(ctx, req)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskDropIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx, req)
		if err != nil {
			return nil, countOut{}, err
		}
		n, err := d.Store.DropStationTasks(ctx, p.StationID, in.TaskIDs, in.Reason, in.HumanDecided, p.ActorID)
		return nil, countOut{Count: n}, err
	})

	addTool(s, d, &mcp.Tool{
		Name:        "station_task_reopen",
		Description: "Reopen closed or dropped tasks — a decision to drop is sometimes wrong.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskReopenIn) (*mcp.CallToolResult, countOut, error) {
		p, err := requireStation(ctx, req)
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
			"session on another machine needs to reconstitute you: memory and instruction files, conventions. Never secrets." +
			" Tool preferences belong here too.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, lockerListOut, error) {
		p, err := requireLocker(ctx, req)
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
			"cannot tell, your human can read it, and it goes into every backup." +
			" Those belong in the VAULT (station_vault_put), which exists for exactly that. Ken cannot inspect a blob and know it is a secret, so this rule is yours to keep.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in lockerPutIn) (*mcp.CallToolResult, lockerMeta, error) {
		p, err := requireLocker(ctx, req)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in lockerGetIn) (*mcp.CallToolResult, lockerGetOut, error) {
		p, err := requireLocker(ctx, req)
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in lockerGetIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireLocker(ctx, req)
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
			" NEVER values: reading one is a separate, logged call. Entries marked with deleted_at are recoverable.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, vaultListOut, error) {
		p, err := requireLocker(ctx, req)
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
			"anything here from the console, and the value is ENCRYPTED AT REST under a key held outside the " +
			"database and outside every backup — so a copy of the database that leaves the host is useless without " +
			"it, while anyone with root on the host can still read both. Writes are reversible — an overwrite keeps " +
			"the previous value. To hand a secret to a session on another machine, use station_vault_send rather " +
			"than pasting it into a message.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in vaultPutIn) (*mcp.CallToolResult, vaultPutOut, error) {
		p, err := requireLocker(ctx, req)
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
		Description: "Read one secret back. THIS CALL IS LOGGED — the secret's name, the time, and whether it was " +
			"read from a station or the console appear in your human's console, which is the point of keeping " +
			"credentials here rather than in a file. Use the value; do not repeat it into the conversation unless " +
			"you have to.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in vaultGetIn) (*mcp.CallToolResult, vaultGetOut, error) {
		p, err := requireLocker(ctx, req)
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
		Name: "station_vault_send",
		Description: "Hand one of your secrets to ANOTHER station's vault — the safe way to give a credential to a " +
			"session on a different machine. THE VALUE NEVER LEAVES THE SERVER: it is re-encrypted straight into " +
			"their vault, so it does not enter a message, a file, or either of our transcripts. NEVER paste a " +
			"credential into comm_send instead; message bodies are stored, retained, and readable by anyone with " +
			"the transcript. Requires an APPROVED LINK between the two stations — the same approval that lets you " +
			"message them, so there is no second ceremony. You KEEP your copy; this is a copy, not a move. Both " +
			"sides are logged: your read is recorded as a transfer, and their copy records who wrote it. " +
			"THEY ARE NOT NOTIFIED — tell them over comm_send, by NAME, never with the value.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in vaultSendIn) (*mcp.CallToolResult, vaultSendOut, error) {
		p, err := requireLocker(ctx, req)
		if err != nil {
			return nil, vaultSendOut{}, err
		}
		e, dropped, err := d.Store.SendStationVaultSecret(ctx, d.vaultLim(), p.StationID,
			strings.TrimSpace(in.ToStation), in.Name, in.AsName, p.TokenID, p.ActorID)
		if err != nil {
			return nil, vaultSendOut{}, err
		}
		return nil, vaultSendOut{
			Name: e.Name, ToStation: strings.TrimSpace(in.ToStation), Bytes: e.SizeBytes,
			SHA256: e.SHA256, Rev: e.Rev, HistoryDropped: dropped,
			Note: "Delivered into their vault as \"" + e.Name + "\". They are NOT notified — tell them it is " +
				"there and what it is for, by name. Do not repeat the value in that message; the sha256 above " +
				"is how you both confirm you hold the same secret without either of you saying it.",
		}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "station_vault_delete",
		Description: "Retire a secret. It stops being readable immediately and stays RECOVERABLE from your human's " +
			"console — deliberately unlike station_locker_delete, which destroys. Rotating a credential is a put, not " +
			"a delete then a put." +
			" Nothing here is destroyed, so a mistake costs your human a click rather than the credential.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in vaultGetIn) (*mcp.CallToolResult, okOut, error) {
		p, err := requireLocker(ctx, req)
		if err != nil {
			return nil, okOut{}, err
		}
		err = d.Store.DeleteStationVaultSecret(ctx, d.vaultLim(), p.StationID, in.Name, p.TokenID, p.ActorID)
		return nil, okOut{OK: err == nil}, err
	})

	addTool(s, d, &mcp.Tool{
		Name:        "ken_version",
		Description: version.ToolDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in version.InstructionsIn) (*mcp.CallToolResult, version.Info, error) {
		out := version.Current()
		// THE ARGUMENT IS THE ESCAPE HATCH FOR SESSIONS THAT CANNOT SEE ken_instructions.
		// Whole tools do not travel across the freeze; parameters do, because the server
		// validates what ARRIVES rather than the client's captured schema. So a session
		// frozen before ken_instructions existed can still ask for the current text here.
		if in.Wants() {
			i := version.InstructionsFor("/station/mcp", instructions)
			out.Instructions = &i
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "ken_instructions",
		Description: version.InstructionsToolDescription,
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, version.InstructionsInfo, error) {
		return nil, version.InstructionsFor("/station/mcp", instructions), nil
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
func requireLocker(ctx context.Context, req *mcp.CallToolRequest) (*principal, error) {
	return requireStation(ctx, req)
}

// hearsayFor computes the write-time hearsay marking (§7) — whether this session had
// recently received COMM traffic. Keyed on the ACTOR, which is why a station key must be
// minted under the same actor as that machine's comm token: a different actor silently
// defeats the marker, and a marker that fails open without saying so is worse than none.
//
// With COMM unavailable there is nothing to check and the marking is simply absent, which is
// "no signal" rather than "known clean" — the same stance COMM's own provenance takes.
func hearsayFor(ctx context.Context, d Deps, p *principal) bool {
	if d.Hearsay == nil {
		return false
	}
	return d.Hearsay(ctx, p.ActorID)
}

// versionNote travels with every station_me result and explains what the number is FOR, because a
// bare version in a briefing is a fact with no instruction attached.
//
// It says the thing this project keeps re-learning: instructions and tool descriptions pin at
// CONNECT time and never refresh, so a running session's manual can be arbitrarily old while its
// tool RESULTS are always current. Parameters cross that freeze; whole tools do not.
const versionNote = "This is the version RUNNING NOW. Your connect-time instructions state the version that " +
	"wrote them; if they differ, that text and every tool description you hold are older — they were " +
	"captured when this conversation began and never refresh. New PARAMETERS still work if you learn " +
	"about them; new TOOLS are not in your list at all and cannot be called. Results like this one are " +
	"always current."

// stationMe resolves the caller to a workspace and returns their briefing, MINTING the workspace
// when the caller has none — the two paths the version stamp must cover, kept in one function so
// the handler above has a single success exit to stamp.
func stationMe(ctx context.Context, d Deps, req *mcp.CallToolRequest, in meIn) (meOut, error) {
	if p := principalFrom(ctx); p != nil && p.StationID == "" && workspaceFrom(req) == "" {
		return claimWorkspace(ctx, d, p, in.WorkspaceName)
	}
	p, err := requireStation(ctx, req)
	if err != nil {
		return meOut{}, err
	}
	if in.SelfDescribedAbout != "" || in.SelfDescribedTags != nil {
		if err := d.Store.SetStationSelfDescription(ctx, p.StationID, in.SelfDescribedAbout, in.SelfDescribedTags); err != nil {
			return meOut{}, err
		}
	}
	return buildBriefing(ctx, d, p)
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
	// KenVersion/VersionNote are NOT set here — the station_me handler stamps them on every path,
	// including the workspace-creation one this function is not on. See the comment there.
	out := meOut{
		StationID: st.StationID, Name: st.Name, NameSource: "human", Purpose: st.Purpose,
		SelfDescribedAbout: st.SelfDescribedAbout, SelfDescribedTags: st.SelfDescribedTags,
		Tasks: briefingView{
			Open: b.OpenTotal, BlockedOnHuman: b.BlockedOnHuman, Overdue: b.Overdue,
			NotBriefedRecently: b.AgingCount, BriefedUnchanged: b.StuckCount,
			DeferredRepeatedly: b.RepeatedlyDefer, Remainder: b.Remainder,
			NeverBriefed: b.NeverBriefed, OldestBlockedDays: b.OldestBlockedDays,
			StaleRisk:      b.StaleRisk,
			OldestOpenDays: b.OldestOpenDays,
			Head:           taskViews(b.Head),
		},
	}
	// WHICH COMM ENDPOINT IS MINE. Absent when COMM is unavailable.
	//
	// THE ORIGINAL NOTE HERE CLAIMED A DISTINCTION THIS CODE DOES NOT MAKE, and ken-prod-ops
	// caught it by noticing the field in their own briefing and not in a station-only session's.
	// It said the field is "omitted, not emptied, because 'COMM is not running here' and 'you are
	// bound to no endpoint' would otherwise look identical". They serialize identically anyway:
	// the json tag is `omitempty`, which drops an empty slice as readily as a nil one, so a
	// station with zero endpoints and a server with COMM off produce the same absent field.
	//
	// Left as-is deliberately, with the reasoning corrected rather than the behaviour: neither
	// state asserts anything false, and "no endpoint to tell you about" is the honest reading of
	// both. What was wrong was the comment claiming a signal the wire never carried.
	//
	// A lookup failure is NOT fatal: this briefing carries open tasks and what is waiting on
	// a human, and losing all of that because a secondary lookup failed would be a poor
	// trade. It is omitted instead, which reads the same as COMM being off — acceptable
	// precisely because neither state asserts anything false.
	if d.CommEndpoints != nil {
		if ids, err := d.CommEndpoints(ctx, p.StationID); err != nil {
			log.Printf("stations: comm endpoints for %s: %v", p.StationID, err)
		} else {
			out.CommEndpointIDs = ids
		}
	}
	switch {
	case activities < 0:
		out.Handoff = "no handoff page yet — write one as you go, not on the way out, and keep it with mode='replace' rather than append"
	case activities == 0:
		out.Handoff = "handoff current (written " + writtenAt + ")"
	default:
		out.Handoff = fmt.Sprintf("handoff written %s, %d activities ago", writtenAt, activities)
	}
	// WHAT IS WAITING ON THIS HUMAN SOMEWHERE ELSE. A PURE READ — nothing here stamps
	// last_briefed_at, and it must stay that way: this caller does not staff those stations
	// and cannot relay their contents, so marking them briefed would record a briefing that
	// never happened and suppress the item for the session that could actually give one.
	//
	// Non-fatal, like the endpoint lookup above and for the same reason: losing a whole
	// briefing because a secondary count failed is a bad trade, and an omitted field reads
	// as "nothing elsewhere", which asserts nothing false.
	if n, stations, err := d.Store.HumanBlockedElsewhere(ctx, p.StationID); err != nil {
		log.Printf("stations: cross-station count for %s: %v", p.StationID, err)
	} else if n > 0 {
		out.Elsewhere = &elsewhereView{Tasks: n, Stations: stations,
			Note: "You staff one station; these are on others. You cannot read them and you cannot " +
				"check them — blocked_on is set once at creation and nothing revisits it, so some of " +
				"these are already done. Tell your human the NUMBER and send them to /stations, which " +
				"is the only place the whole pile is visible. Do not tell them they still owe these."}
	}
	switch {
	case b.BlockedOnHuman > 0 || b.Overdue > 0:
		out.Relay = "Tell your human, in words, in your first message: " +
			fmt.Sprintf("%d task(s) are waiting on them and %d are past their date.", b.BlockedOnHuman, b.Overdue)
		if out.Elsewhere != nil {
			out.Relay += fmt.Sprintf(" Also mention that %d more are recorded as waiting on them across %d other station(s) — send them to /stations rather than listing what you cannot see.",
				out.Elsewhere.Tasks, out.Elsewhere.Stations)
		}
	case out.Elsewhere != nil:
		// NOTHING HERE AND SOMETHING THERE is the case this whole field exists for: without
		// it the session says "nothing is waiting on you" while another station's pile grows
		// unmentioned, and that answer is worse than silence.
		out.Relay = fmt.Sprintf("Tell your human, in words, in your first message: nothing is waiting on them HERE, but %d task(s) are recorded as waiting on them across %d other station(s) — the whole pile is at /stations.",
			out.Elsewhere.Tasks, out.Elsewhere.Stations)
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
		ActorID: sp.ActorID, TokenID: sp.TokenID, StationID: sp.StationID, Scopes: scopes,
	})
}

// claimWorkspace mints a workspace for a session that has none, names it after the folder, and
// hands back the id the human must write into that folder's MCP entry.
//
// docs/IDENTITY.md §5, decided: "fully working, auto-named, no approval." Nothing is withheld and
// nothing is pending — the session works from the next call.
//
// THE NAME IS A HINT, NOT AN IDENTITY. Routing is by id, always: COMM.md §3's rule, learned on
// endpoints — "display labels are non-unique decoration ... a human-chosen name is never an
// address, or the first release ships a global namespace one session can squat." So a collision is
// resolved by decorating the NAME and never by refusing, and the human renames it in the console
// whenever they like without invalidating a single config.
func claimWorkspace(ctx context.Context, d Deps, p *principal, hint string) (meOut, error) {
	name := strings.TrimSpace(hint)
	if name == "" {
		// A session that offered no folder name still gets a workspace — withholding one over a
		// missing hint would put the deadlock back for the exact case it was built for.
		name = "workspace"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	st, err := d.Store.CreateStationAutoNamed(ctx, name, p.ActorID)
	if err != nil {
		return meOut{}, err
	}
	log.Printf("STATION: minted workspace %s (%q, auto-named) for token %s — no approval required (IDENTITY.md §5)",
		st.StationID, st.Name, p.TokenID)
	return meOut{
		StationID:   st.StationID,
		Name:        st.Name,
		NameSource:  "auto",
		JustCreated: true,
		PutThisInYourConfig: "Ken made you a workspace called " + st.Name + " and you are working in it NOW — " +
			"notebook, tasks, locker and vault, nothing withheld and nothing to approve. TWO THINGS TO DO: " +
			"(1) tell your human, in words, that you are working as " + st.Name + " (auto-named after this folder) " +
			"and that they can rename it in the console if it is wrong — the name is what they will see when " +
			"approving this workspace to talk to another, which is the one moment a bad name matters. " +
			"(2) ask them to add the header " + WorkspaceHeader + ": " + st.StationID + " to this folder's Ken MCP " +
			"entry. Without it the NEXT session here starts with no workspace and mints a second one. The id is " +
			"permanent and is not a secret — it is a name tag, not a key.",
	}, nil
}
