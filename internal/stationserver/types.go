package stationserver

// Tool input/output shapes. Schemas are derived from these by reflection, so the
// jsonschema tags ARE the agent-facing documentation.
//
// The station surface sits outside the byte-level compatibility contract while it is
// opt-in and off by default (COMPATIBILITY.md), so these shapes evolve ADDITIVELY:
// new optional fields, never a removed or retyped one.
//
// Note the field NAMES on a station's self-description. `self_described_about` carries
// the untrustworthiness in the name itself, because a sibling `verified:false` key does
// not survive a harness flattening a structured result into prose — and once flattened,
// a claim reads as fact (S8).

// dirEntry mirrors comm_directory's row on the /station surface. The claim fields
// keep their claim-bearing names (S8) so a reader that flattens this still sees that
// self_described_about is what the other station says, not what a human vouched for.
type dirEntry struct {
	Name               string   `json:"name"`
	Purpose            string   `json:"purpose,omitempty"`
	SelfDescribedAbout string   `json:"self_described_about,omitempty"`
	SelfDescribedTags  []string `json:"self_described_tags,omitempty"`
	Linked             bool     `json:"linked"`
	// Omitted entirely when COMM cannot answer. See Deps.Staffing.
	Staffed    *bool  `json:"staffed,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

type dirIn struct{}

type dirOut struct {
	Stations []dirEntry `json:"stations"`
	YouAre   string     `json:"you_are,omitempty"`
	// CommKnown is false when this deployment has COMM off, so a reader can tell
	// "reachability is unknown here" from "everyone is idle".
	CommKnown bool `json:"comm_known"`
}

type meIn struct {
	SelfDescribedAbout string   `json:"self_described_about,omitempty" jsonschema:"optional; how YOU describe what you know and are responsible for. A CLAIM, shown to others as self-described"`
	SelfDescribedTags  []string `json:"self_described_tags,omitempty" jsonschema:"optional; short self-declared topic tags"`
}

type briefingView struct {
	Open               int `json:"open"`
	BlockedOnHuman     int `json:"blocked_on_human"`
	Overdue            int `json:"overdue"`
	NotBriefedRecently int `json:"not_briefed_recently"`
	BriefedUnchanged   int `json:"briefed_repeatedly_unchanged"`
	DeferredRepeatedly int `json:"deferred_repeatedly"`
	Remainder          int `json:"not_shown"`
	// NeverBriefed is how many open tasks have NEVER appeared in a briefing head.
	//
	// `not_shown` reads as a queue waiting its turn. This is how many have never had one —
	// the head holds at most seven, so on a station with forty open tasks the same handful
	// surfaces and the rest are invisible to the session and the human alike.
	NeverBriefed int `json:"never_briefed" jsonschema:"open tasks that have never appeared in any briefing. If this is large, the list you are being shown is not a summary of your work — it is a sample of it"`
	// OldestBlockedDays ages the human-blocked pile. A bare count carries no urgency and
	// no way to tell a fresh request from one that has sat for a month.
	OldestBlockedDays int `json:"oldest_blocked_on_human_days" jsonschema:"age in days of the longest-standing item blocked on your human"`
	// StaleRisk is the population most likely to be ALREADY DONE and still counted.
	StaleRisk int `json:"blocked_on_human_and_stale" jsonschema:"items blocked on your human that have not been briefed in over a week. blocked_on is set once at creation and nothing revisits it, so these are the ones most likely to be finished already — CHECK THE UNDERLYING STATE before you say your human still owes them"`
	// OldestOpenDays ages EVERY open task, not only the human-blocked ones. NOT omitempty:
	// zero is an answer ("nothing here is old"), and dropping it would make that
	// indistinguishable from a deployment too old to have the field.
	OldestOpenDays int        `json:"oldest_open_task_days" jsonschema:"age in days of the longest-standing OPEN task, whatever it is blocked on — including your own. A task can be entirely accurate and no longer the point, and nothing else in this briefing can see that: oldest_blocked_on_human_days is gated on blocked_on='human', and briefed_count only rises, so an item briefed every session looks attended to. If this is large, read the full list with station_task_list and look at created_at. Nothing is deferred or closed for you: this figure surfaces, you and your human decide"`
	Head           []taskView `json:"head"`
}

type meOut struct {
	StationID          string       `json:"station_id"`
	Name               string       `json:"name"`
	NameSource         string       `json:"name_source"` // always "human": no tool writes a station name
	Purpose            string       `json:"purpose"`
	SelfDescribedAbout string       `json:"self_described_about"`
	SelfDescribedTags  []string     `json:"self_described_tags,omitempty"`
	Tasks              briefingView `json:"tasks"`
	Handoff            string       `json:"handoff"`
	Relay              string       `json:"relay_to_human,omitempty"`
	// KenVersion is what is running RIGHT NOW, reported in the briefing every session is
	// told to call FIRST.
	//
	// It is here because the ken_version TOOL cannot reach the sessions that most need
	// it. A tool added in 3.1.0 is absent from the tool list of every conversation begun
	// before 3.1.0, and there is no handle to call something you cannot see — parameters
	// travel across the freeze, whole tools do not. A RESULT is the only channel that
	// always arrives, so the answer rides in one.
	// CommEndpointIDs are the comm endpoints bound to this station — the answer to "which
	// endpoint_id should I be calling comm_* with?".
	//
	// Here because station_me is the call every session is already instructed to make FIRST,
	// so a pre-flight that costs nothing is cheapest in the place it is already mandatory.
	// A session whose credentials file names an endpoint absent from this list is using
	// somebody else's, which is a thing that happened and which nothing could detect.
	//
	// Omitted entirely when COMM is off, never reported as empty: empty would mean "you are
	// bound to nothing", which is a different and more alarming fact.
	CommEndpointIDs []string `json:"comm_endpoint_ids,omitempty" jsonschema:"the comm endpoint ids bound to this station. If the endpoint_id in your credentials file is not in this list, you are using another session's credentials"`
	KenVersion      string   `json:"ken_version"`
	// VersionNote says what to do with it, because a bare number in a briefing is a
	// number a session reads past.
	VersionNote string `json:"ken_version_note"`
}

type requestIn struct {
	Purpose  string `json:"purpose" jsonschema:"required; what this station is FOR. Your human approves on this, not on the name"`
	NameHint string `json:"name_hint,omitempty" jsonschema:"optional; a suggestion only — your human types the real name"`
}
type requestOut struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Guidance  string `json:"guidance"`
}

type noteMeta struct {
	Key       string   `json:"key"`
	Title     string   `json:"title,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Rev       int      `json:"rev"`
	Bytes     int      `json:"bytes"`
	UpdatedAt string   `json:"updated_at"`
	// RevisionsLost is how many of this page's revisions the history bound has already
	// deleted, oldest first. NOT omitempty: zero is the answer that matters most, and
	// dropping it would make "nothing lost" and "the field does not exist" identical.
	//
	// Pruning is silent by design and a session cannot otherwise discover it. A live
	// station was measured at head rev 26 holding only revisions 18 and up — seventeen
	// gone, including its original context, with nothing anywhere reporting it.
	RevisionsLost int `json:"revisions_lost"`
	// HistoryBytes is what this page's retained revisions occupy. A page kept by APPEND
	// accumulates history proportional to the SQUARE of its length, so this number goes
	// bad long before `bytes` does — one measured station reached 96% of its cap with
	// 252,759 bytes of history behind an 8,083-byte page.
	HistoryBytes int `json:"history_bytes"`
}
type noteListOut struct {
	Pages []noteMeta `json:"pages"`
}
type noteReadIn struct {
	Key string `json:"key" jsonschema:"required; the page key. 'handoff' is the reserved page a future session reads first"`
	// Rev reads a RETAINED older revision. Omit for the current page.
	//
	// Added because station_note_list could tell a station it had lost history and
	// nothing let it look at what remained — a measurement with no read path leaves a
	// session knowing something is wrong and unable to act on it.
	Rev int `json:"rev,omitempty" jsonschema:"optional; read a retained older revision instead of the current page. Revisions below the oldest retained have been pruned and say so"`
}
type noteWriteIn struct {
	Key   string   `json:"key" jsonschema:"required"`
	Title string   `json:"title,omitempty"`
	Body  string   `json:"body" jsonschema:"required"`
	Tags  []string `json:"tags,omitempty"`
	Mode  string   `json:"mode,omitempty" jsonschema:"append (default) or replace"`
	IfRev int      `json:"if_rev,omitempty" jsonschema:"optional; the rev you read. The write is refused if the page moved, so a second session staffing this station is not silently clobbered"`
}
type noteOut struct {
	Key       string   `json:"key"`
	Title     string   `json:"title,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Body      string   `json:"body,omitempty"`
	Rev       int      `json:"rev"`
	Bytes     int      `json:"bytes"`
	UpdatedAt string   `json:"updated_at"`
}
type promoteOut struct {
	PromotionID string `json:"promotion_id"`
	State       string `json:"state"`
	Guidance    string `json:"guidance"`
}

type taskView struct {
	TaskID           string `json:"task_id"`
	Text             string `json:"text"`
	Detail           string `json:"detail,omitempty"`
	BlockedOn        string `json:"blocked_on"`
	BlockedOnStation string `json:"blocked_on_station,omitempty"`
	RemindAfter      string `json:"remind_after,omitempty"`
	State            string `json:"state"`
	CreatedAt        string `json:"created_at"`
	LastBriefedAt    string `json:"last_briefed_at,omitempty"`
	BriefedCount     int    `json:"briefed_count"`
	DeferredUntil    string `json:"deferred_until,omitempty"`
	DeferCount       int    `json:"defer_count"`
	Resolution       string `json:"resolution,omitempty"`
	ResolutionLink   string `json:"resolution_link,omitempty"`
	StationName      string `json:"station_name,omitempty"`
	// Named for what it is: the writing session had recently received inter-session
	// traffic, so this commitment may be another session's idea (§7).
	HearsayAtWrite bool `json:"written_while_receiving_peer_traffic,omitempty"`
}

type taskAddIn struct {
	Text        string `json:"text" jsonschema:"required; one line, imperative"`
	BlockedOn   string `json:"blocked_on" jsonschema:"required; self = you can act now, human = it cannot move until your human does or decides, peer = another station owes something"`
	Detail      string `json:"detail,omitempty" jsonschema:"optional; why, and what done looks like"`
	Context     string `json:"context,omitempty" jsonschema:"optional; what this arose from"`
	RemindAfter string `json:"remind_after,omitempty" jsonschema:"optional ISO-8601 date; nothing before it, prominent after"`
}
type taskAddOut struct {
	Task        taskView   `json:"task"`
	NearMatches []taskView `json:"near_matches,omitempty" jsonschema:"open tasks that look similar — if one is the same commitment, close the duplicate and name the id you kept in its resolution"`
}
type taskListIn struct {
	State     string `json:"state,omitempty" jsonschema:"open (default) | done | dropped"`
	BlockedOn string `json:"blocked_on,omitempty" jsonschema:"filter: self | human | peer"`
	Limit     int    `json:"limit,omitempty"`
}
type taskListOut struct {
	Tasks []taskView `json:"tasks"`
	Total int        `json:"total"`
	Shown int        `json:"shown"`
}
type taskCloseIn struct {
	TaskIDs        []string `json:"task_ids" jsonschema:"required; several at once"`
	Resolution     string   `json:"resolution" jsonschema:"required; one line on what happened"`
	ResolutionLink string   `json:"resolution_link,omitempty" jsonschema:"optional; a knowledge-base slug, a commit, or a URL"`
}
type taskDeferIn struct {
	TaskID string `json:"task_id" jsonschema:"required"`
	Until  string `json:"until" jsonschema:"required ISO-8601 date"`
	Reason string `json:"reason" jsonschema:"required; deferring costs more than closing on purpose"`
}
type taskDropIn struct {
	TaskIDs      []string `json:"task_ids" jsonschema:"required"`
	Reason       string   `json:"reason" jsonschema:"required"`
	HumanDecided bool     `json:"human_decided,omitempty" jsonschema:"set only when your human themselves decided to abandon it — required for anything blocked on them"`
}
type taskReopenIn struct {
	TaskIDs []string `json:"task_ids" jsonschema:"required"`
	Reason  string   `json:"reason" jsonschema:"required"`
}

type lockerMeta struct {
	Name        string `json:"name"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}
type lockerListOut struct {
	Files []lockerMeta `json:"files"`
}
type lockerPutIn struct {
	Name        string `json:"name" jsonschema:"required; a flat label, never a path"`
	Body        string `json:"body" jsonschema:"required; text. NEVER a token, key or password"`
	ContentType string `json:"content_type,omitempty"`
}
type lockerGetIn struct {
	Name string `json:"name" jsonschema:"required"`
}
type lockerGetOut struct {
	Name   string `json:"name"`
	Body   string `json:"body"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// --- vault ---------------------------------------------------------------
//
// vaultMeta is what every surface EXCEPT a read is allowed to see. There is no
// `secret` field on it by construction rather than by discipline: a listing that
// could carry a value would eventually carry one.
type vaultMeta struct {
	Name      string `json:"name"`
	Note      string `json:"note,omitempty"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	Rev       int    `json:"rev"`
	ReadCount int    `json:"read_count"`
	UpdatedAt string `json:"updated_at,omitempty"`
	// Non-empty means this name is a tombstone. The value is still recoverable from the
	// console, which is why the field says when rather than whether.
	DeletedAt string `json:"deleted_at,omitempty"`
}
type vaultListOut struct {
	Secrets []vaultMeta `json:"secrets"`
}
type vaultPutIn struct {
	Name   string `json:"name" jsonschema:"required; a flat label, never a path"`
	Secret string `json:"secret" jsonschema:"required; the credential itself"`
	Note   string `json:"note,omitempty" jsonschema:"what this is and where it came from — shown to your human INSTEAD of the value"`
}
type vaultPutOut struct {
	vaultMeta
	// HistoryDropped is how many recoverable older values this write pushed out of the
	// bound. Non-zero is worth repeating to your human: those values are gone.
	HistoryDropped int `json:"history_dropped,omitempty"`
}
type vaultGetIn struct {
	Name string `json:"name" jsonschema:"required"`
}
type vaultGetOut struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
	Note   string `json:"note,omitempty"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	// ReadCount includes this read. It is here so a session can notice a secret being
	// read far more often than its own use explains.
	ReadCount int `json:"read_count"`
}

type countOut struct {
	Count int `json:"count"`
}
type okOut struct {
	OK bool `json:"ok"`
}

// voucherIn names the endpoint that will redeem the voucher, and nothing else.
//
// There is deliberately still NO station_id argument: the station is decided by the
// key in the Authorization header, never by anything the model says, or a session
// could ask to be bound to a station it holds no key for.
//
// EndpointID is the opposite case — it is safe as an argument precisely because it
// is not a credential. It NARROWS the voucher rather than widening it: naming an
// endpoint you do not control mints a voucher you cannot use, because redeeming it
// still requires that endpoint's secret. There is nothing to gain by lying here.
type voucherIn struct {
	EndpointID string `json:"endpoint_id" jsonschema:"required; the endpoint_id this voucher will bind — your OWN, from comm_register. The voucher is usable by that endpoint alone, so a leaked one is inert. Register and save your secret FIRST, then ask for the voucher"`
}

type voucherOut struct {
	BindingVoucher string `json:"binding_voucher"`
	ExpiresInSec   int    `json:"expires_in_seconds"`
	StationID      string `json:"station_id"`
	StationName    string `json:"station_name"`
	// ForEndpoint echoes the nomination so a session can see WHICH endpoint it just
	// tied the voucher to. A voucher minted for the wrong endpoint id fails at
	// comm_bind with a refusal that reads like a leak; echoing it turns that into a
	// typo the session can spot before it calls.
	ForEndpoint string `json:"for_endpoint"`
}

type linkRequestIn struct {
	ToStation string `json:"to_station" jsonschema:"required; the station you want to be able to talk to, by NAME as your human refers to it"`
	Reason    string `json:"reason" jsonschema:"required; why this relationship should exist. Written for YOUR HUMAN, who decides — it is never shown to the other station before they approve, so do not address it to them"`
}

type linkRequestOut struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}
