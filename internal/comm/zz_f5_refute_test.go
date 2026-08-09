package comm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fillHubN builds a hub with n channels and puts exactly `per` messages on each.
func fillHubN(t *testing.T, st *Store, n, per, bodyBytes int) (*Endpoint, []*Endpoint, []string) {
	t.Helper()
	ctx := context.Background()
	hub, spokes, chans := hubFanIn(t, st, n)
	body := strings.Repeat("y", bodyBytes)
	for i, sp := range spokes {
		for j := 0; j < per; j++ {
			if _, err := st.Send(ctx, sp, chans[i], body, SendOpts{}); err != nil {
				t.Fatalf("send %d/%d: %v", i, j, err)
			}
		}
	}
	return hub, spokes, chans
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

// TestF5PollCostScaling measures the actual per-message cost of Poll, unbound
// and station-bound, against a real hub-sized backlog.
func TestF5PollCostScaling(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	hub, _, _ := fillHubN(t, st, 15, 64, 256) // the claim's 960-message backlog

	measure := func(label string, ep *Endpoint) {
		for _, limit := range []int{1, 10, 50, 100} {
			if _, err := st.Poll(ctx, ep, limit); err != nil {
				t.Fatal(err)
			}
			const reps = 30
			var ds []time.Duration
			got := 0
			for i := 0; i < reps; i++ {
				t0 := time.Now()
				ms, err := st.Poll(ctx, ep, limit)
				ds = append(ds, time.Since(t0))
				if err != nil {
					t.Fatal(err)
				}
				got = len(ms)
			}
			t.Logf("%s Poll(limit=%d) -> %d msgs: p50=%v max=%v | per-msg %v",
				label, limit, got, pct(ds, .5), pct(ds, 1),
				time.Duration(int64(pct(ds, .5))/int64(max(got, 1))))
		}
	}
	measure("unbound", hub)

	if err := st.BindEndpointToStation(ctx, hub.EndpointID, "stn_hub", "k1"); err != nil {
		t.Fatal(err)
	}
	measure("bound  ", &Endpoint{ID: hub.ID, EndpointID: hub.EndpointID, StationID: "stn_hub"})
}

// TestF5PollCostVsBacklog is the claim's central assertion, stated as testable:
// "the write lock is taken proportional to how much mail it is behind on".
func TestF5PollCostVsBacklog(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{1, 2, 8, 15, 30} {
		st := newStore(t, DefaultLimits())
		hub, _, _ := fillHubN(t, st, n, 64, 256)
		if _, err := st.Poll(ctx, hub, 50); err != nil {
			t.Fatal(err)
		}
		var ds []time.Duration
		for i := 0; i < 20; i++ {
			t0 := time.Now()
			if _, err := st.Poll(ctx, hub, 50); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(t0))
		}
		t.Logf("N=%2d channels, backlog=%4d: Poll(50) p50=%v max=%v", n, n*64, pct(ds, .5), pct(ds, 1))
		st.Close()
	}
}

// TestF5PerOpCosts puts Poll next to the other writes it supposedly starves.
func TestF5PerOpCosts(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 100000
	st := newStore(t, l)
	a, b, ch := pair(t, st)

	var sends []time.Duration
	var ids []string
	for i := 0; i < 300; i++ {
		t0 := time.Now()
		m, err := st.Send(ctx, a, ch, "x", SendOpts{IdempotencyKey: fmt.Sprintf("k%d", i)})
		sends = append(sends, time.Since(t0))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.MessageID)
	}
	var polls1 []time.Duration
	for i := 0; i < 50; i++ {
		t0 := time.Now()
		if _, err := st.Poll(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
		polls1 = append(polls1, time.Since(t0))
	}
	var acks []time.Duration
	for _, id := range ids {
		t0 := time.Now()
		if err := st.Ack(ctx, b, id); err != nil {
			t.Fatal(err)
		}
		acks = append(acks, time.Since(t0))
	}
	t.Logf("Send      p50=%v max=%v", pct(sends, .5), pct(sends, 1))
	t.Logf("Ack       p50=%v max=%v", pct(acks, .5), pct(acks, 1))
	t.Logf("Poll(1)   p50=%v max=%v", pct(polls1, .5), pct(polls1, 1))
}

// TestF5BatchingIsCheaper: is the N+1 loop a cost or a saving? Compare the
// writer time to move 960 messages in polls of 50 vs polls of 1.
func TestF5BatchingIsCheaper(t *testing.T) {
	ctx := context.Background()
	run := func(limit int) (time.Duration, int, int) {
		st := newStore(t, DefaultLimits())
		defer st.Close()
		hub, _, _ := fillHubN(t, st, 15, 64, 256)
		t0 := time.Now()
		polls, got := 0, 0
		for {
			ms, err := st.Poll(ctx, hub, limit)
			if err != nil {
				t.Fatal(err)
			}
			polls++
			if len(ms) == 0 {
				break
			}
			for _, m := range ms {
				if err := st.Ack(ctx, hub, m.MessageID); err != nil {
					t.Fatal(err)
				}
				got++
			}
		}
		return time.Since(t0), polls, got
	}
	for _, limit := range []int{1, 10, 50, 100} {
		d, polls, got := run(limit)
		t.Logf("drain 960 with Poll(%3d): %d polls, %d acked, total writer time %v", limit, polls, got, d)
	}
}

// TestF5WriterContention: how long does a spoke's send ACTUALLY wait while a hub
// polls 50 at a time -- in a tight loop, far more aggressive than a model client.
func TestF5WriterContention(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 100000
	st := newStore(t, l)
	hub, spokes, chans := fillHubN(t, st, 16, 64, 256)
	probeSpoke, probeChan := spokes[15], chans[15]

	probe := func(tag string, reps int) []time.Duration {
		var ds []time.Duration
		for i := 0; i < reps; i++ {
			t0 := time.Now()
			_, err := st.Send(ctx, probeSpoke, probeChan, "probe", SendOpts{
				IdempotencyKey: fmt.Sprintf("%s-%d", tag, i),
			})
			ds = append(ds, time.Since(t0))
			if err != nil {
				t.Fatalf("probe send: %v", err)
			}
		}
		return ds
	}

	base := probe("base", 150)
	t.Logf("comm_send latency, NO poller           : p50=%v p95=%v max=%v", pct(base, .5), pct(base, .95), pct(base, 1))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var polls int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := st.Poll(ctx, hub, 50); err != nil {
				return
			}
			polls++
		}
	}()
	cont := probe("cont", 150)
	close(stop)
	wg.Wait()
	t.Logf("comm_send latency, hub Poll(50) tight loop (%d polls): p50=%v p95=%v max=%v",
		polls, pct(cont, .5), pct(cont, .95), pct(cont, 1))

	// And with the hub at the hard cap, bound (claim UPDATE included).
	if err := st.BindEndpointToStation(ctx, hub.EndpointID, "stn_hub", "k1"); err != nil {
		t.Fatal(err)
	}
	bhub := &Endpoint{ID: hub.ID, EndpointID: hub.EndpointID, StationID: "stn_hub"}
	stop2 := make(chan struct{})
	var polls2 int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop2:
				return
			default:
			}
			if _, err := st.Poll(ctx, bhub, 100); err != nil {
				return
			}
			polls2++
		}
	}()
	cont2 := probe("cont2", 150)
	close(stop2)
	wg.Wait()
	t.Logf("comm_send latency, BOUND hub Poll(100) tight loop (%d polls): p50=%v p95=%v max=%v",
		polls2, pct(cont2, .5), pct(cont2, .95), pct(cont2, 1))
}
