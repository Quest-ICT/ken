// Package settings holds Ken's operator-editable runtime configuration. Values
// come from env/compiled defaults, are overridden by rows in app_setting (written
// by the web UI), and are exposed as an atomically-swapped Snapshot that live
// consumers (rate limiter, login guard, client-IP resolver, ACME host policy)
// read without a restart.
package settings

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Quest-ICT/ken/internal/clientip"
	"github.com/Quest-ICT/ken/internal/store"
)

// Values are the raw, editable settings.
type Values struct {
	RLEnabled   bool
	IPPerMin    int
	IPBurst     int
	TokenPerMin int
	TokenBurst  int
	BlockAfter  int
	LockoutSec  int
	AllowCIDRs  string

	LoginMaxFails   int
	LoginLockoutSec int
	SessionTTLHours int

	TrustedProxies string

	TLSMode    string // read-only display (a mode switch needs a listener restart)
	TLSDomains string // acme hostnames — live
	TLSEmail   string // acme account email — live (affects new registrations)

	// CurationLangs is the operator's comma-separated list of language codes the
	// human curator can read (e.g. "fr,zh"). Blank ⇒ the feature is off: agents get
	// no language guidance and nothing is flagged. Stored verbatim; the normalized
	// set consumers read is derived into Snapshot.CurationLangSet.
	CurationLangs string

	// Inter-session communication (COMM) limits. COMM is always on and cannot be
	// switched off; these apply live. They are inert only in the degraded case where
	// comm.db could not be opened at all, which is a failure rather than a choice.
	//
	// They are bounds on an EPHEMERAL subsystem that shares a disk with the durable
	// knowledge base, so the defaults are deliberately conservative: the failure
	// they guard against is message traffic filling the volume and failing KB writes.
	CommMaxBodyBytes      int
	CommMaxUnacked        int
	CommMessageTTLSec     int
	CommUndeliveredTTLSec int
	CommBodyRetentionSec  int
	CommMetadataTTLSec    int
	CommReplyDeadlineS    int
	CommPollWaitMaxSec    int
	// CommProvenanceWindowSec is how recently a token must have RECEIVED an
	// inter-session message for a version it authors to be marked as possible
	// hearsay (docs/COMM.md §7). 0 disables the marking.
	CommProvenanceWindowSec int

	// File exchange (docs/COMM.md §11) — gated separately from COMM itself because
	// the byte relay is the bulk of the subsystem's risk. Sizes are in MB in the
	// form (an operator thinks in MB); converted to bytes where enforced.
	CommFilesEnabled  bool
	CommFileMaxMB     int
	CommFileBudgetMB  int
	CommFileMinFreeMB int
	CommFileTTLSec    int
	CommGrantTTLSec   int
	// CommClaimLeaseSec is how long a station-bound reader holds a claimed message
	// before it returns to its station's unclaimed tail (docs/STATIONS.md S4). It
	// bounds how long a session that claimed a message and then died can strand it
	// from the station's other readers, so it is sized against a MODEL TURN rather
	// than a request. Only bound endpoints are affected; unbound traffic never
	// claims anything.
	CommClaimLeaseSec int

	// Station bounds (docs/STATIONS.md §9). Every one of these is a BACKUP decision
	// before it is a storage decision: station assets live in ken.db, so whatever a
	// station may hold, the nightly snapshot copies — and S12's ×15 multiplier is the
	// reason the reasons are attached rather than the numbers standing alone.
	StationNotePageKiB     int
	StationNoteRevisionKiB int
	StationNotebookKiB     int
	StationLockerBlobKiB   int
	StationLockerTotalKiB  int
	StationMaxOpenTasks    int
	StationTaskTextBytes   int
	StationTaskDetailBytes int
	StationTaskListLimit   int
	StationVaultSecretKiB  int
	StationVaultEntries    int
	StationVaultHistoryRev int
	StationVaultReadLog    int
}

// Snapshot is Values plus the derived objects consumers read.
type Snapshot struct {
	Values
	Resolver  *clientip.Resolver
	AllowNets []*net.IPNet
	Domains   []string
	// CurationLangSet is the normalized curation languages (lowercased BCP-47
	// PRIMARY subtags, de-duplicated, in order) — read by the MCP instructions
	// and (later) the review-queue guardrail. Empty ⇒ feature off.
	CurationLangSet []string
}

func buildSnapshot(v Values) *Snapshot {
	return &Snapshot{
		Values:          v,
		Resolver:        clientip.NewResolver(v.TrustedProxies),
		AllowNets:       clientip.ParseCIDRs(v.AllowCIDRs),
		Domains:         splitList(v.TLSDomains),
		CurationLangSet: normLangs(v.CurationLangs),
	}
}

// Field describes one editable setting: how to render it and how to parse/validate
// it into Values.
type Field struct {
	Key, Group, Label, Help, Type string // type: int | bool | cidrs | domains | langs | email | text | enum
	Live                          bool   // applies live vs needs a restart
	ReadOnly                      bool   // display only
	Get                           func(Values) string
	Set                           func(*Values, string) error
}

func intField(key, group, label, help string, get func(Values) int, set func(*Values, int), min, max int) Field {
	return Field{
		Key: key, Group: group, Label: label, Help: help, Type: "int", Live: true,
		Get: func(v Values) string { return strconv.Itoa(get(v)) },
		Set: func(v *Values, s string) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return fmt.Errorf("must be a number")
			}
			if n < min || n > max {
				return fmt.Errorf("must be between %d and %d", min, max)
			}
			set(v, n)
			return nil
		},
	}
}

// Fields is the ordered registry that drives both the form and validation.
var Fields = []Field{
	{Key: "rl_enabled", Group: "Rate limiting", Label: "Enabled", Type: "bool", Live: true,
		Help: "Turns the whole abuse guard on or off — per-IP and per-token limits, auto-blocking, the lot. Off means every request reaches the application.",
		Get:  func(v Values) string { return boolStr(v.RLEnabled) },
		Set:  func(v *Values, s string) error { v.RLEnabled = truthy(s); return nil }},
	intField("rl_ip_rpm", "Rate limiting", "Per-IP requests / minute", "Sustained per-IP request rate.",
		func(v Values) int { return v.IPPerMin }, func(v *Values, n int) { v.IPPerMin = n }, 1, 1_000_000),
	intField("rl_ip_burst", "Rate limiting", "Per-IP burst", "Bucket size — absorbs short bursts (page loads).",
		func(v Values) int { return v.IPBurst }, func(v *Values, n int) { v.IPBurst = n }, 1, 1_000_000),
	intField("rl_token_rpm", "Rate limiting", "Per-token requests / minute", "Per agent token (MCP).",
		func(v Values) int { return v.TokenPerMin }, func(v *Values, n int) { v.TokenPerMin = n }, 1, 1_000_000),
	intField("rl_token_burst", "Rate limiting", "Per-token burst", "Bucket size for one agent token — absorbs a session firing several calls in one turn without counting them against its sustained rate.",
		func(v Values) int { return v.TokenBurst }, func(v *Values, n int) { v.TokenBurst = n }, 1, 1_000_000),
	intField("rl_block_after", "Rate limiting", "Auto-block after", "Consecutive over-limit rejections before an IP is blocked (0 = never block).",
		func(v Values) int { return v.BlockAfter }, func(v *Values, n int) { v.BlockAfter = n }, 0, 1_000_000),
	intField("rl_lockout_sec", "Rate limiting", "Auto-block lockout (seconds)", "How long an auto-blocked IP stays blocked. Blocking is a 403 returned before the request enters the chain, so a blocked caller costs nothing to refuse.",
		func(v Values) int { return v.LockoutSec }, func(v *Values, n int) { v.LockoutSec = n }, 1, 7*24*3600),
	{Key: "rl_allow_cidrs", Group: "Rate limiting", Label: "Always-allowed CIDRs", Type: "cidrs", Live: true,
		Help: "Comma-separated CIDRs exempt from rate limiting (loopback is always exempt).",
		Get:  func(v Values) string { return v.AllowCIDRs },
		Set:  setCIDRs(func(v *Values, s string) { v.AllowCIDRs = s })},

	intField("login_max_fails", "Login", "Max failed logins", "Failures from one IP before a lockout.",
		func(v Values) int { return v.LoginMaxFails }, func(v *Values, n int) { v.LoginMaxFails = n }, 1, 10000),
	intField("login_lockout_sec", "Login", "Login lockout (seconds)", "How long an IP is refused after too many failed logins. Applies per realm, so locking out of one console does not lock you out of another.",
		func(v Values) int { return v.LoginLockoutSec }, func(v *Values, n int) { v.LoginLockoutSec = n }, 1, 7*24*3600),
	intField("session_ttl_hours", "Session", "Session lifetime (hours)", "New sessions only.",
		func(v Values) int { return v.SessionTTLHours }, func(v *Values, n int) { v.SessionTTLHours = n }, 1, 24*30),

	{Key: "trusted_proxies", Group: "Network", Label: "Trusted proxy CIDRs", Type: "cidrs", Live: true,
		Help: "X-Forwarded-For is honored only from these peers. Blank = none. Sensitive: over-broad values let a client forge its IP.",
		Get:  func(v Values) string { return v.TrustedProxies },
		Set:  setCIDRs(func(v *Values, s string) { v.TrustedProxies = s })},

	{Key: "tls_mode", Group: "TLS", Label: "TLS mode", Type: "enum", ReadOnly: true,
		Help: "Set via KEN_TLS in the unit; a mode switch (off/acme/file) needs a service restart.",
		Get:  func(v Values) string { return v.TLSMode }},
	{Key: "tls_domains", Group: "TLS", Label: "ACME domains", Type: "domains", Live: true,
		Help: "Comma-separated hostnames the Let's Encrypt cert is issued for. Live (acme mode); a new host is issued on demand.",
		Get:  func(v Values) string { return v.TLSDomains },
		Set:  setDomains(func(v *Values, s string) { v.TLSDomains = s })},
	{Key: "tls_email", Group: "TLS", Label: "ACME account email", Type: "email", ReadOnly: true,
		Help: "Set via KEN_TLS_EMAIL; used when registering the Let's Encrypt account (not editable here).",
		Get:  func(v Values) string { return v.TLSEmail }},

	{Key: "curation_langs", Group: "Curation", Label: "Curation language(s)", Type: "langs", Live: true,
		Help: "Comma-separated language codes you can read (e.g. fr,zh). Agents are told to author entries in these so you can review and promote them; proposals outside them are flagged on the review queue. Blank = off.",
		Get:  func(v Values) string { return v.CurationLangs },
		Set:  setLangs(func(v *Values, s string) { v.CurationLangs = s })},

	// COMM. Every field is Live: these are read per operation, so an operator can
	// tighten a limit while a runaway channel is in progress rather than after a
	// restart. Nothing here can enable COMM — that stays a restart-level choice.
	intField("comm_max_body_bytes", "Inter-session comms", "Max message size (bytes)",
		"Message bodies are atomic; there is no multi-part send. Keep this small — tool arguments are generated token by token by a model, so even 64 KiB is a five-figure token count.",
		func(v Values) int { return v.CommMaxBodyBytes }, func(v *Values, n int) { v.CommMaxBodyBytes = n }, 256, 1<<20),
	intField("comm_max_unacked", "Inter-session comms", "Max unacknowledged per channel",
		"Backpressure. Past this a send is refused so two auto-processing sessions cannot loop unboundedly.",
		func(v Values) int { return v.CommMaxUnacked }, func(v *Values, n int) { v.CommMaxUnacked = n }, 1, 100_000),
	intField("comm_message_ttl_sec", "Inter-session comms", "Lifetime after delivery (seconds)",
		"How long a DELIVERED but unacknowledged message survives. The clock starts when the recipient is first handed the message, not when it was sent — a session goes 16 h between pulls on a weeknight and 64 h over a weekend, so a send-anchored clock expired mail during the exact window nobody could read it.",
		func(v Values) int { return v.CommMessageTTLSec }, func(v *Values, n int) { v.CommMessageTTLSec = n }, 60, 30*24*3600),
	intField("comm_undelivered_ttl_sec", "Inter-session comms", "Lifetime before delivery (seconds)",
		"Backstop for a message NOBODY has ever polled — it only bounds mail addressed to an endpoint that never comes back. Undelivered mail is bounded first by the per-channel unacknowledged cap, which is a volume limit and therefore immune to how long a human is away. Set this longer than the longest absence you expect: a fortnight's leave is 400 h.",
		func(v Values) int { return v.CommUndeliveredTTLSec }, func(v *Values, n int) { v.CommUndeliveredTTLSec = n }, 3600, 90*24*3600),
	intField("comm_body_retention_sec", "Inter-session comms", "Body retention after settling (seconds)",
		"How long a message's TEXT survives after it is acknowledged or expires. ZERO restores the old behaviour of deleting the body the moment it is acknowledged — which destroyed 97% of one deployment's message bodies through the ordinary poll/act/acknowledge path, because the unacknowledged inbox was the only place a body ever existed.",
		func(v Values) int { return v.CommBodyRetentionSec }, func(v *Values, n int) { v.CommBodyRetentionSec = n }, 0, 90*24*3600),
	intField("comm_metadata_ttl_sec", "Inter-session comms", "Metadata retention (seconds)",
		"How long a settled message's row survives after it was created. NOTE: since bodies now survive acknowledgement (see Body retention), this is also the ONLY bound on a body that was never delivered — retention cannot reclaim one, because a message nobody received has no settle time to measure from. Raising this for a longer audit trail multiplies retained undelivered bytes with it.",
		func(v Values) int { return v.CommMetadataTTLSec }, func(v *Values, n int) { v.CommMetadataTTLSec = n }, 60, 90*24*3600),
	intField("comm_reply_deadline_sec", "Inter-session comms", "Reply deadline (seconds)",
		"Default deadline for a message that requires a response; past it the sender is told the reply is overdue instead of waiting forever.",
		func(v Values) int { return v.CommReplyDeadlineS }, func(v *Values, n int) { v.CommReplyDeadlineS = n }, 30, 7*24*3600),
	intField("comm_poll_wait_max_sec", "Inter-session comms", "Max long-poll wait (seconds)",
		"Ceiling on how long a receive call may block. Clamped to 30 in code regardless: a wait that ties the client's tool timeout turns a successful empty poll into an error.",
		func(v Values) int { return v.CommPollWaitMaxSec }, func(v *Values, n int) { v.CommPollWaitMaxSec = n }, 1, 30),
	intField("comm_provenance_window_sec", "Inter-session comms", "Hearsay window (seconds)",
		"If a token received an inter-session message this recently, entries it authors are flagged on the review queue as possibly second-hand. 0 disables the flag.",
		func(v Values) int { return v.CommProvenanceWindowSec }, func(v *Values, n int) { v.CommProvenanceWindowSec = n }, 0, 7*24*3600),

	// File exchange. A live off-switch on purpose: turning it off mid-incident
	// stops bytes immediately without a restart.
	//
	// THE HELP SAID "Off by default" UNTIL 2026-08-25, and it had been wrong since the default
	// flipped on 2026-08-24. internal/comm/comm.go:200 records the flip and its reason — Vlad's
	// ruling that no Ken feature is optional or off by default — so the code and the text
	// disagreed about the one thing an operator reads this line to learn. Found by an audit for
	// exactly that class; the same claim was live in docs/OPERATION.md and docs/COMM.md.
	{Key: "comm_files_enabled", Group: "Inter-session comms", Label: "File exchange enabled", Type: "bool", Live: true,
		Help: "Lets paired sessions exchange files (same-host handoff, or relayed through Ken). ON by default, like every Ken feature. Turn it OFF to stop the relay writing bytes to this server's disk — it applies immediately, with no restart, which is what makes it useful mid-incident.",
		Get:  func(v Values) string { return boolStr(v.CommFilesEnabled) },
		Set:  func(v *Values, s string) error { v.CommFilesEnabled = truthy(s); return nil }},
	intField("comm_file_max_mb", "Inter-session comms", "Max file size (MB)",
		"Cap on one relayed or offered file.",
		func(v Values) int { return v.CommFileMaxMB }, func(v *Values, n int) { v.CommFileMaxMB = n }, 1, 1024),
	intField("comm_file_budget_mb", "Inter-session comms", "Relay storage budget (MB)",
		"Global cap on bytes held in the relay at once. The relay shares this server's disk with the knowledge base; filling it would fail durable writes over chat traffic.",
		func(v Values) int { return v.CommFileBudgetMB }, func(v *Values, n int) { v.CommFileBudgetMB = n }, 1, 100_000),
	intField("comm_file_min_free_mb", "Inter-session comms", "Free-space floor (MB)",
		"Uploads are refused when the disk has less than this free, even under budget, so the knowledge base always has headroom. 0 disables the floor.",
		func(v Values) int { return v.CommFileMinFreeMB }, func(v *Values, n int) { v.CommFileMinFreeMB = n }, 0, 1_000_000),
	intField("comm_file_ttl_sec", "Inter-session comms", "File lifetime (seconds)",
		"How long an offered or undelivered file survives before it expires and its bytes are deleted.",
		func(v Values) int { return v.CommFileTTLSec }, func(v *Values, n int) { v.CommFileTTLSec = n }, 60, 30*24*3600),
	intField("comm_grant_ttl_sec", "Inter-session comms", "Transfer grant lifetime (seconds)",
		"How long a one-time upload/download URL stays valid. Short is right: it only has to survive being handed to curl.",
		func(v Values) int { return v.CommGrantTTLSec }, func(v *Values, n int) { v.CommGrantTTLSec = n }, 60, 3600),
	intField("comm_claim_lease_sec", "Inter-session comms", "Station claim lease (seconds)",
		"When several sessions staff one station, the first to poll a message claims it and the others do not see it. This is how long that claim lasts before the message returns to the queue for another session to pick up. It only matters if you use stations; too short and a session still working gets undercut, too long and a session that died holds its messages out of reach.",
		func(v Values) int { return v.CommClaimLeaseSec }, func(v *Values, n int) { v.CommClaimLeaseSec = n }, 30, 3600),

	// --- Stations (docs/STATIONS.md §9) -------------------------------------------
	// The help text states the REASON for each bound, not the number: a bound whose
	// reason is attached is one an operator can raise deliberately, and one whose
	// reason is missing gets raised by a hundred because a session complained.
	intField("station_note_page_kib", "Stations", "Notebook page size (KiB)",
		"The largest a single notebook page may be. A page bigger than this is a document rather than a note, and notebook pages travel in every backup.",
		func(v Values) int { return v.StationNotePageKiB }, func(v *Values, n int) { v.StationNotePageKiB = n }, 1, 1024),
	intField("station_note_revision_kib", "Stations", "Revision history per page (KiB)",
		"How much edit history one page keeps before the oldest revisions are dropped. This is an undo buffer, not an archive — it exists so a session can recover from its own bad overwrite.",
		func(v Values) int { return v.StationNoteRevisionKiB }, func(v *Values, n int) { v.StationNoteRevisionKiB = n }, 0, 4096),
	intField("station_notebook_kib", "Stations", "Notebook per station (KiB)",
		"Total size of a station's current notebook pages, excluding history. Roughly sixty full pages at the default page size.",
		func(v Values) int { return v.StationNotebookKiB }, func(v *Values, n int) { v.StationNotebookKiB = n }, 64, 65536),
	intField("station_locker_blob_kib", "Stations", "Locker file size (KiB)",
		"The largest single file a station may store. The locker is for memory and instruction files, not payloads.",
		func(v Values) int { return v.StationLockerBlobKiB }, func(v *Values, n int) { v.StationLockerBlobKiB = n }, 1, 4096),
	intField("station_locker_total_kib", "Stations", "Locker per station (KiB)",
		"Total locker storage one station may use. Everything here lands verbatim in your backups, and Ken cannot inspect it.",
		func(v Values) int { return v.StationLockerTotalKiB }, func(v *Values, n int) { v.StationLockerTotalKiB = n }, 16, 65536),
	intField("station_vault_secret_kib", "Stations", "Vault secret size (KiB)",
		"The largest single credential a station may store. A PEM private key fits; anything larger is a file, and files belong in the locker.",
		func(v Values) int { return v.StationVaultSecretKiB }, func(v *Values, n int) { v.StationVaultSecretKiB = n }, 1, 256),
	intField("station_vault_entries", "Stations", "Secrets per station",
		"How many credentials one station may hold. Everything here is stored UNENCRYPTED and travels in every backup — the protection is the machine and the backup, not Ken.",
		func(v Values) int { return v.StationVaultEntries }, func(v *Values, n int) { v.StationVaultEntries = n }, 1, 1000),
	intField("station_vault_history_rev", "Stations", "Vault versions kept per secret",
		"How many superseded values stay recoverable after an overwrite or a delete. This is what makes a vault write reversible; at 0 an overwrite is final.",
		func(v Values) int { return v.StationVaultHistoryRev }, func(v *Values, n int) { v.StationVaultHistoryRev = n }, 0, 200),
	intField("station_vault_read_log", "Stations", "Vault reads kept per station",
		"How many read records the audit trail retains. The per-secret read COUNT is exact regardless, so lowering this shortens the trail without hiding how often something was read.",
		func(v Values) int { return v.StationVaultReadLog }, func(v *Values, n int) { v.StationVaultReadLog = n }, 10, 100000),
	intField("station_max_open_tasks", "Stations", "Open tasks per station",
		"How many tasks a station may have open at once. A list longer than this is not being worked, and refusing is more honest than letting it grow.",
		func(v Values) int { return v.StationMaxOpenTasks }, func(v *Values, n int) { v.StationMaxOpenTasks = n }, 10, 5000),
	intField("station_task_text_bytes", "Stations", "Task line length (bytes)",
		"A task's one-line summary and its resolution line. Short by construction: anything needing more belongs in the detail field or a notebook page.",
		func(v Values) int { return v.StationTaskTextBytes }, func(v *Values, n int) { v.StationTaskTextBytes = n }, 64, 4096),
	intField("station_task_detail_bytes", "Stations", "Task detail size (bytes)",
		"The optional detail and context attached to a task. A task needing more than this is really a notebook page with a plan.",
		func(v Values) int { return v.StationTaskDetailBytes }, func(v *Values, n int) { v.StationTaskDetailBytes = n }, 256, 65536),
	intField("station_task_list_limit", "Stations", "Task list page size",
		"The default AND maximum number of tasks one listing returns. It bounds how much of the pile a session can pull into its context at once.",
		func(v Values) int { return v.StationTaskListLimit }, func(v *Values, n int) { v.StationTaskListLimit = n }, 5, 200),
}

// Live holds the current snapshot atomically and applies edits.
type Live struct {
	ptr      atomic.Pointer[Snapshot]
	store    *store.Store
	defaults Values
	mu       sync.Mutex // serializes Apply
	onChange []func(*Snapshot)
}

// New builds a Live seeded with defaults (env-derived). Call Load to fold in the
// persisted overrides.
func New(st *store.Store, defaults Values) *Live {
	l := &Live{store: st, defaults: defaults}
	l.ptr.Store(buildSnapshot(defaults))
	return l
}

// Current returns the effective snapshot.
func (l *Live) Current() *Snapshot { return l.ptr.Load() }

// Defaults returns the compiled/env defaults (before persisted overrides).
func (l *Live) Defaults() Values { return l.defaults }

// OnChange registers a listener fired (synchronously) whenever the snapshot swaps.
func (l *Live) OnChange(f func(*Snapshot)) { l.onChange = append(l.onChange, f) }

// Load reads persisted overrides on top of the defaults and swaps them in.
func (l *Live) Load(ctx context.Context) error {
	overrides, err := l.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	v := l.defaults
	for _, f := range Fields {
		if f.ReadOnly || f.Set == nil {
			continue
		}
		if raw, ok := overrides[f.Key]; ok {
			_ = f.Set(&v, raw) // tolerate a stale/invalid stored value; keep the default
		}
	}
	l.swap(buildSnapshot(v))
	return nil
}

// Apply validates the submitted form, persists the diffs from default, and swaps
// FieldError is a validation failure that names its field by KEY rather than by
// label, because the two are not the same string on the operator's screen.
//
// The settings form resolves every label through the translation bundle, with the Go
// registry text only as a fallback. A message built here out of f.Label therefore
// names the field as the CODE calls it, not as the FORM shows it — which is how an
// error came to say "Lower Lifetime after delivery first" on a page whose field was
// labelled "Message lifetime". Carrying the key instead lets the renderer resolve
// each name exactly as it rendered the form, in whatever language that was.
//
// Message may contain {0} for Key's label and {1}, {2}… for Refs', matching the
// placeholder style the bundles already use. It contains no field names of its own.
//
// KNOWN LIMIT, stated rather than implied: the REASON text is still English. It comes
// from Go errors returned by each field's Set, and translating those is a separate
// piece of work. So a Spanish operator gets Spanish field names inside an English
// sentence — which is strictly better than an English name they cannot find on the
// page, and worse than the finished thing.
type FieldError struct {
	// Key is the field the operator must change.
	Key string
	// Refs are other fields the Message mentions, in {1}, {2}… order.
	Refs []string
	// Message is the reason, with {n} placeholders and no field names.
	Message string
	// Standalone means Message already names Key via {0}, so a renderer must not
	// prefix it. A per-field error is the opposite: its Message is a bare reason and
	// wants "<label>: " in front.
	Standalone bool
}

// the new snapshot in live. Returns the resulting snapshot and any field errors
// (on error nothing is persisted or applied).
func (l *Live) Apply(ctx context.Context, form map[string]string, updater string) (*Snapshot, []FieldError) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cand := l.defaults
	cand.TLSMode = l.Current().TLSMode // read-only field keeps its runtime value
	var errs []FieldError
	upsert := map[string]string{}
	var remove []string
	for _, f := range Fields {
		if f.ReadOnly || f.Set == nil {
			continue
		}
		raw := form[f.Key] // absent (e.g. unchecked checkbox) -> ""
		if err := f.Set(&cand, raw); err != nil {
			// Key, never f.Label: the label here is the RAW registry string, and the
			// form renders labels through the translation bundle. Naming the field
			// from this side misnames it for anyone whose bundle says something else
			// — which, before this, was 2 of 43 fields in English and 31 of 43 in
			// Spanish and French.
			errs = append(errs, FieldError{Key: f.Key, Message: err.Error()})
			continue
		}
		// Removal is decided ONLY by equality to the default; a value that differs is
		// persisted verbatim — including an empty string (so clearing a field with a
		// non-empty default sticks across a restart, not just live).
		if f.Get(cand) == f.Get(l.defaults) {
			remove = append(remove, f.Key)
		} else {
			upsert[f.Key] = f.Get(cand)
		}
	}
	// CROSS-FIELD RULES. Per-field ranges cannot express "this one must exceed that
	// one", so they run here, after every field has parsed and before anything is
	// persisted.
	errs = append(errs, crossFieldErrors(cand)...)
	if len(errs) > 0 {
		return l.Current(), errs
	}
	if err := l.store.SetSettings(ctx, upsert, remove, updater); err != nil {
		// No Key: this is not about a field the operator typed, so a renderer must
		// not try to label it.
		return l.Current(), []FieldError{{Message: "could not save: " + err.Error(), Standalone: true}}
	}
	snap := buildSnapshot(cand)
	l.swap(snap)
	return snap, nil
}

// crossFieldErrors validates relationships between settings that no per-field range
// can express.
//
// The one rule today is the one that bit an operator: an undelivered backstop BELOW
// the post-delivery TTL is nonsense — mail would die before its real clock could
// start — and the store used to handle it by silently substituting a default. The
// console kept showing the operator's number while the running system used another,
// which is the failure this project keeps finding: a remedy that is inert with
// nothing to distinguish it from one that worked. On a deployment already running a
// 30-day message TTL that was every value the form accepts below 30 days.
//
// Refuse and name the other field, so the operator can see what to change.
//
// NAMING IT IS THE WHOLE POINT, AND THE FIRST VERSION GOT IT WRONG IN A WAY THAT
// DEFEATED ITSELF. It wrote the labels as literal English taken from this file —
// "Lower Lifetime after delivery first" — while the form renders labels through the
// translation bundle. So it named a field that appeared nowhere on the operator's
// screen, and the field they actually had to change was sitting directly above the
// message under a different name. An error that names the wrong thing is the same
// class of defect as one that fires on the wrong condition.
//
// Field names are therefore PLACEHOLDERS resolved by whoever renders this. {0} is
// Key, {1} onward are Refs, and every one is looked up the way the form looks it up.
func crossFieldErrors(v Values) []FieldError {
	var errs []FieldError
	if v.CommUndeliveredTTLSec > 0 && v.CommUndeliveredTTLSec < v.CommMessageTTLSec {
		errs = append(errs, FieldError{
			Key:  "comm_undelivered_ttl_sec",
			Refs: []string{"comm_message_ttl_sec"},
			Message: fmt.Sprintf(
				"{0} (%ds) must be at least {1} (%ds) — "+
					"a message would otherwise expire while it is still waiting to be picked up, before its own clock could start. "+
					"Lower {1} first, then set this.",
				v.CommUndeliveredTTLSec, v.CommMessageTTLSec),
			Standalone: true,
		})
	}
	return errs
}

func (l *Live) swap(s *Snapshot) {
	l.ptr.Store(s)
	for _, f := range l.onChange {
		f(s)
	}
}

// --- defaults from env (mirrors the components' own FromEnv defaults) ---

// DefaultsFromEnv builds the baseline from the same env vars the components read,
// so the settings UI starts from what the operator configured at launch.
func DefaultsFromEnv() Values {
	v := Values{
		RLEnabled:       envBool("KEN_RATELIMIT", true),
		IPPerMin:        envInt("KEN_RATELIMIT_IP_RPM", 120),
		IPBurst:         envInt("KEN_RATELIMIT_IP_BURST", 120),
		TokenPerMin:     envInt("KEN_RATELIMIT_TOKEN_RPM", 120),
		TokenBurst:      envInt("KEN_RATELIMIT_TOKEN_BURST", 60),
		BlockAfter:      envInt("KEN_RATELIMIT_BLOCK_AFTER", 100),
		LockoutSec:      envInt("KEN_RATELIMIT_LOCKOUT_SEC", 900),
		AllowCIDRs:      strings.TrimSpace(os.Getenv("KEN_RATELIMIT_ALLOW_CIDRS")),
		LoginMaxFails:   5,
		LoginLockoutSec: 300,
		SessionTTLHours: 12,
		TrustedProxies:  strings.TrimSpace(os.Getenv("KEN_TRUSTED_PROXIES")),
		TLSMode:         strings.ToLower(strings.TrimSpace(envOr("KEN_TLS", "off"))),
		TLSDomains:      strings.TrimSpace(os.Getenv("KEN_TLS_DOMAINS")),
		TLSEmail:        strings.TrimSpace(os.Getenv("KEN_TLS_EMAIL")),
		CurationLangs:   strings.TrimSpace(os.Getenv("KEN_CURATION_LANGS")),

		// COMM defaults mirror comm.DefaultLimits(). They are duplicated rather than
		// imported because internal/settings must not depend on an optional
		// subsystem — the settings registry is loaded on every boot, COMM is not.
		// The comm package's own defaults remain the source of truth for a caller
		// that constructs a store directly (tests); a drift here changes only what a
		// fresh install starts from, never what an already-configured one uses.
		CommMaxBodyBytes:        64 * 1024,
		CommMaxUnacked:          64,
		CommMessageTTLSec:       24 * 3600,
		CommUndeliveredTTLSec:   30 * 24 * 3600,
		CommBodyRetentionSec:    24 * 3600,
		CommMetadataTTLSec:      7 * 24 * 3600,
		CommReplyDeadlineS:      3600,
		CommPollWaitMaxSec:      15,
		CommProvenanceWindowSec: 3600,

		CommFilesEnabled:  true,
		CommFileMaxMB:     16,
		CommFileBudgetMB:  256,
		CommFileMinFreeMB: 512,
		CommFileTTLSec:    24 * 3600,
		CommGrantTTLSec:   300,

		// §9 says fifteen minutes, and the reasoning is better than the five I first
		// wrote: the lease has to outlast a MODEL TURN, not a request. A session that
		// claims a message and then does a long piece of work must not have it taken
		// away mid-thought, because the second reader would act on it too — which is
		// the duplicate-action failure claim-once exists to prevent.
		CommClaimLeaseSec: 900,

		StationNotePageKiB:     64,
		StationNoteRevisionKiB: 256,
		StationNotebookKiB:     4096,
		StationLockerBlobKiB:   256,
		StationLockerTotalKiB:  2048,
		StationMaxOpenTasks:    500,
		StationTaskTextBytes:   512,
		StationTaskDetailBytes: 4096,
		StationTaskListLimit:   50,
		StationVaultSecretKiB:  8,
		StationVaultEntries:    64,
		StationVaultHistoryRev: 16,
		StationVaultReadLog:    500,
	}
	// Clamp env-provided values so a bad env can't silently disable the limiter or overflow.
	if v.IPPerMin <= 0 {
		v.IPPerMin = 120
	}
	if v.IPBurst <= 0 {
		v.IPBurst = 120
	}
	if v.TokenPerMin <= 0 {
		v.TokenPerMin = 120
	}
	if v.TokenBurst <= 0 {
		v.TokenBurst = 60
	}
	if v.BlockAfter < 0 {
		v.BlockAfter = 0
	}
	if v.LockoutSec <= 0 || v.LockoutSec > 7*24*3600 {
		v.LockoutSec = 900
	}
	return v
}

// --- small helpers ---

func setCIDRs(assign func(*Values, string)) func(*Values, string) error {
	return func(v *Values, s string) error {
		s = strings.TrimSpace(s)
		for _, c := range strings.Split(s, ",") {
			if c = strings.TrimSpace(c); c == "" {
				continue
			}
			_, ipnet, err := net.ParseCIDR(c)
			if err != nil {
				return fmt.Errorf("%q is not a valid CIDR (e.g. 10.0.0.0/8)", c)
			}
			// A /0 matches the whole internet — trusting every peer (spoofable XFF)
			// or exempting everyone from the limiter. Always a misconfiguration here.
			if ones, _ := ipnet.Mask.Size(); ones == 0 {
				return fmt.Errorf("%q is too broad (a /0 matches every address) — narrow it", c)
			}
		}
		assign(v, s)
		return nil
	}
}

// langTag matches a lenient BCP-47 tag: a 2–3 letter primary subtag, optionally
// followed by '-'/'_'-separated region/script subtags (which normLangs discards).
var langTag = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{1,8})*$`)

// setLangs validates a comma-separated list of BCP-47 language codes. Blank is
// allowed (feature off). The operator's text is stored verbatim; buildSnapshot
// derives the normalized CurationLangSet that consumers actually read.
func setLangs(assign func(*Values, string)) func(*Values, string) error {
	return func(v *Values, s string) error {
		s = strings.TrimSpace(s)
		for _, t := range strings.Split(s, ",") {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if !langTag.MatchString(t) {
				return fmt.Errorf("%q is not a language code (use e.g. en, es, fr, zh)", t)
			}
		}
		assign(v, s)
		return nil
	}
}

// normLangs derives the canonical curation-language set from the operator's text:
// lowercased BCP-47 PRIMARY subtags (region/script suffixes dropped: en-US→en,
// zh_Hans→zh), de-duplicated, in order. This is what the AI instructions and the
// review-queue guardrail read.
func normLangs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if i := strings.IndexAny(t, "-_"); i > 0 {
			t = t[:i]
		}
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func setDomains(assign func(*Values, string)) func(*Values, string) error {
	return func(v *Values, s string) error {
		s = strings.TrimSpace(s)
		for _, d := range strings.Split(s, ",") {
			if d = strings.TrimSpace(d); d == "" {
				continue
			}
			if strings.ContainsAny(d, " \t\n/:") || !strings.Contains(d, ".") {
				return fmt.Errorf("%q is not a valid hostname", d)
			}
		}
		assign(v, s)
		return nil
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return ""
}
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func envBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "":
		return def
	case "0", "off", "false", "no":
		return false
	default:
		return true
	}
}
func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
