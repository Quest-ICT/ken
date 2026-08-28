package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/store"
)

// TestRevokingAStationKeySaysTheCountIsUnknownWhenCommIsOff IS DELETED. Nothing binds by a station
// key, so the count it guarded is structurally zero and the control is gone.

// TestRevokingAStationKeyStatesHowManySessionsItSevers IS DELETED, AND THE COUNT IT GUARDED IS NOW
// STRUCTURALLY ZERO. It required the /tokens revoke confirmation to state how many live sessions a
// station-key revocation would sever — a real S6 property while a station key AUTHORISED a
// binding. Nothing binds by a station key: a mailbox belongs to its station from the moment it
// exists and no credential authorises that.
//
// The count function and the sever it mirrors are left standing for now and are DEAD: both would
// answer 0 for every key. They belong to the same cleanup as station keys themselves, which the
// single-credential decision retires, and that is a deletion to make deliberately rather than as a
// side effect of this one.

// *** THE CONNECTOR ROW MUST NAME THE CAUSE, NOT JUST THE SYMPTOM. ***
//
// A legacy grant reaches the knowledge base only, whatever URL the connector points at — so a
// session listing kb_* tools and nothing else may be carrying a stale grant rather than a wrong
// URL. Vlad hit exactly that on the live estate and debugged it as a URL mistake, because deleting
// a connector revokes nothing and re-adding silently reuses the grant. The page is where the two
// facts finally meet, so the badge has to render.
func TestTheConnectorRowFlagsALegacyGrantAndShowsItsRedirectHost(t *testing.T) {
	st, ctx, cli, base := oauthHarness(t)

	// A grant approved before per-surface scopes existed, with a loopback redirect — the shape
	// that made an unaccountable client plausible on the live estate.
	clientID, err := st.RegisterOAuthClient(ctx, "old-connector", []string{"http://127.0.0.1:9876/callback"})
	if err != nil {
		t.Fatal(err)
	}
	// A second human to own the grant. The approver's identity is not what this test is about —
	// the badge and the redirect host are — and reusing "admin" collides on the unique name.
	human, err := st.CreateHumanUser(ctx, "approver", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOAuthGrantAndCode(ctx, store.NewAuthCode{
		ClientID:            clientID,
		ConnectorActorID:    human,
		HumanActorID:        human,
		RedirectURI:         "http://127.0.0.1:9876/callback",
		CodeChallenge:       "x",
		CodeChallengeMethod: "S256",
		Scope:               "read write offline_access",
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	body := get(t, cli, base+"/tokens")
	if !strings.Contains(body, "legacy grant") {
		t.Error("a grant with no ken: scope renders with no legacy marker, so the page still shows " +
			"the symptom and hides the cause — which is the failure this exists to prevent")
	}
	if !strings.Contains(body, "127.0.0.1:9876") {
		t.Error("the connector row does not show the registered redirect host — the only field on " +
			"it that carries trust, since the application name is self-reported")
	}
}
