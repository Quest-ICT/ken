package comm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// hub registers one hub endpoint and n delegates, each on its own channel.
func hubFanIn(t *testing.T, st *Store, n int) (*Endpoint, []*Endpoint, []string) {
	t.Helper()
	ctx := context.Background()
	hub, _, err := st.RegisterEndpoint(ctx, owner("tok-hub"), "hub", "")
	if err != nil {
		t.Fatalf("register hub: %v", err)
	}
	var dels []*Endpoint
	var chans []string
	for i := 0; i < n; i++ {
		d, _, err := st.RegisterEndpoint(ctx, owner("tok-d"), "delegate", "")
		if err != nil {
			t.Fatalf("register delegate %d: %v", i, err)
		}
		code, err := st.MintPairingCode(ctx, 1, 42, "hub<->delegate")
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		if _, err := st.JoinChannel(ctx, hub, code); err != nil {
			t.Fatalf("hub join %d: %v", i, err)
		}
		ch, err := st.JoinChannel(ctx, d, code)
		if err != nil {
			t.Fatalf("delegate join %d: %v", i, err)
		}
		if !ch.Open() {
			t.Fatalf("channel %d not open", i)
		}
		dels = append(dels, d)
		chans = append(chans, ch.ChannelID)
	}
	return hub, dels, chans
}

// TestRefuteHubPollVolume measures what one Poll actually hands back when many
// channels converge on one endpoint.
func TestRefuteHubPollVolume(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	lim := DefaultLimits()

	const nDel = 20
	hub, dels, chans := hubFanIn(t, st, nDel)

	// Each delegate fills its channel to the backpressure cap with MAX-SIZE bodies.
	big := strings.Repeat("x", lim.MaxBodyBytes)
	sentPerChan := 0
	for i, d := range dels {
		for j := 0; ; j++ {
			_, err := st.Send(ctx, d, chans[i], big, SendOpts{})
			if err == ErrBackpressure {
				if i == 0 {
					sentPerChan = j
				}
				break
			}
			if err != nil {
				t.Fatalf("send d%d m%d: %v", i, j, err)
			}
		}
	}
	t.Logf("backpressure stopped each delegate at %d unacked messages/channel (cap %d)",
		sentPerChan, lim.MaxUnackedPerChannel)

	for _, limit := range []int{0, 1, 10, 50, 100, 101, 1000} {
		msgs, err := st.Poll(ctx, hub, limit)
		if err != nil {
			t.Fatalf("poll(%d): %v", limit, err)
		}
		// Serialize exactly what the MCP layer would hand the model.
		total := 0
		chanSeen := map[string]int{}
		for _, m := range msgs {
			total += len(m.Body)
			chanSeen[m.ChannelID]++
		}
		blob, _ := json.Marshal(msgs)
		t.Logf("limit=%-5d -> %3d msgs, %8.2f MiB of body, %8.2f MiB serialized, spanning %d channels",
			limit, len(msgs), float64(total)/1048576, float64(len(blob))/1048576, len(chanSeen))
	}
}

// TestRefuteHubPollOrdering traces which channels a single limited poll covers.
func TestRefuteHubPollOrdering(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	const nDel = 20
	hub, dels, chans := hubFanIn(t, st, nDel)

	// 10 small messages per delegate: 200 queued, poll limit 50.
	for i, d := range dels {
		for j := 0; j < 10; j++ {
			if _, err := st.Send(ctx, d, chans[i], "status report", SendOpts{}); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
	}
	msgs, err := st.Poll(ctx, hub, 0) // default limit
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	perChan := map[string]int{}
	maxSeq := int64(0)
	for _, m := range msgs {
		perChan[m.ChannelID]++
		if m.Seq > maxSeq {
			maxSeq = m.Seq
		}
	}
	t.Logf("default poll returned %d of 200 queued, across %d/%d channels, max seq %d",
		len(msgs), len(perChan), nDel, maxSeq)
	for i, c := range chans {
		t.Logf("  channel %2d: %d messages in this poll", i, perChan[c])
	}
}

// TestRefuteSmallLimitIsAvailable checks whether the caller can bound the volume.
func TestRefuteSmallLimitIsAvailable(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	hub, dels, chans := hubFanIn(t, st, 3)
	big := strings.Repeat("y", 64*1024)
	for i, d := range dels {
		for j := 0; j < 5; j++ {
			if _, err := st.Send(ctx, d, chans[i], big, SendOpts{}); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
	}
	msgs, err := st.Poll(ctx, hub, 1)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	total := 0
	for _, m := range msgs {
		total += len(m.Body)
	}
	t.Logf("limit=1 -> %d msgs, %d bytes of body (a caller CAN bound it)", len(msgs), total)
}

// TestRefuteModestCase measures the claim's own "modest" illustration:
// 2 KB status reports from 20 delegates.
func TestRefuteModestCase(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	hub, dels, chans := hubFanIn(t, st, 20)
	body := strings.Repeat("s", 2048)
	for i, d := range dels {
		if _, err := st.Send(ctx, d, chans[i], body, SendOpts{}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	msgs, err := st.Poll(ctx, hub, 0)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	blob, _ := json.Marshal(msgs)
	bodyBytes := 0
	for _, m := range msgs {
		bodyBytes += len(m.Body)
	}
	t.Logf("20 delegates x 2 KB -> %d msgs, %d body bytes, %d serialized bytes (~%d tok at 4B/tok); metadata overhead %d B/msg",
		len(msgs), bodyBytes, len(blob), len(blob)/4, (len(blob)-bodyBytes)/len(msgs))
}

// TestRefuteSeqOrderingAcrossChannels: seq is a PER-CHANNEL counter, yet the poll
// orders by it globally. Does a long-running channel starve behind a new one?
func TestRefuteSeqOrderingAcrossChannels(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	hub, dels, chans := hubFanIn(t, st, 2)

	// Channel 0 is old and chatty: 60 messages, hub acks them all, so its counter
	// is high. Then it sends one more — the OLDEST unacked message in the system.
	for j := 0; j < 60; j++ {
		m, err := st.Send(ctx, dels[0], chans[0], "old-traffic", SendOpts{})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if err := st.Ack(ctx, hub, m.MessageID); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}
	urgent, err := st.Send(ctx, dels[0], chans[0], "URGENT-from-veteran-channel", SendOpts{})
	if err != nil {
		t.Fatalf("send urgent: %v", err)
	}
	t.Logf("veteran channel's next message got seq=%d", urgent.Seq)

	// Channel 1 is brand new and floods 40 messages AFTER the urgent one.
	for j := 0; j < 40; j++ {
		if _, err := st.Send(ctx, dels[1], chans[1], "newcomer-chatter", SendOpts{}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	msgs, err := st.Poll(ctx, hub, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	found := false
	for i, m := range msgs {
		if m.Body == "URGENT-from-veteran-channel" {
			found = true
			t.Logf("urgent message appeared at position %d of %d", i, len(msgs))
		}
	}
	if !found {
		t.Logf("STARVATION: the OLDEST unacked message (seq=%d) is absent from a limit=10 poll; "+
			"all %d slots went to the newer channel's seq 1..N", urgent.Seq, len(msgs))
	}
}
