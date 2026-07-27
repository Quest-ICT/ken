package commserver

import (
	"context"
	"sync"
	"time"
)

// waiters implements long-poll wakeups: a parked comm_poll sleeps on a channel
// until a message arrives for its endpoint, its deadline passes, its request is
// cancelled, or the server starts draining.
//
// Wakeups are an OPTIMIZATION, never the correctness mechanism. Delivery is
// correct because a poll re-reads the database before returning; the wakeup only
// decides how soon that happens. That distinction matters for two reasons: a
// missed signal costs latency rather than a lost message, and wakeups do not cross
// process boundaries — a send handled by one instance cannot wake a poll parked on
// another. COMM therefore assumes a single instance, and multi-instance deployments
// would degrade to poll-interval latency rather than break. See docs/COMM.md §12.
type waiters struct {
	mu       sync.Mutex
	m        map[int64][]chan struct{}
	n        int  // total parked, for the global cap
	draining bool // set once at shutdown; never cleared
}

// Waiter caps. Bounded because nothing outside the process bounds them: the HTTP
// server deliberately sets no write timeout (the streamable transport holds
// long-lived responses) and the systemd unit sets no task limit, so an unbounded
// waiter set is a goroutine and file-descriptor leak waiting for a busy machine.
const (
	maxWaitersPerEndpoint = 2   // a session needs one; two absorbs an overlapping retry
	maxWaitersTotal       = 256 // global backstop on a 1 GB box
)

func newWaiters() *waiters { return &waiters{m: map[int64][]chan struct{}{}} }

// wait parks until a notify for endpointID, the timeout, request cancellation, or
// drain. It reports whether it was woken by a notify (as opposed to timing out),
// which the caller uses only for reporting — it re-reads the database either way.
//
// Returning immediately when the caps are hit is deliberate: an immediate empty
// poll is a correct, cheap answer, whereas queueing would convert a burst into
// held connections.
func (w *waiters) wait(ctx context.Context, endpointID int64, d time.Duration) bool {
	ch := make(chan struct{}, 1)

	w.mu.Lock()
	if w.draining || w.n >= maxWaitersTotal || len(w.m[endpointID]) >= maxWaitersPerEndpoint {
		w.mu.Unlock()
		return false
	}
	w.m[endpointID] = append(w.m[endpointID], ch)
	w.n++
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		list := w.m[endpointID]
		for i, c := range list {
			if c == ch {
				w.m[endpointID] = append(list[:i], list[i+1:]...)
				w.n--
				break
			}
		}
		if len(w.m[endpointID]) == 0 {
			delete(w.m, endpointID)
		}
	}()

	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// notify wakes every poll parked on endpointID. Non-blocking: each channel is
// buffered, so a waiter that has already left cannot stall the sender.
func (w *waiters) notify(endpointID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.m[endpointID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// drain wakes every parked poll and refuses new ones. Called before HTTP shutdown
// because the graceful-shutdown budget is shorter than a long poll: without this,
// every deploy would sever parked connections mid-response and surface a burst of
// transport errors in each connected agent. Woken pollers return a normal empty
// result, which clients are told to treat as ordinary.
func (w *waiters) drain() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.draining = true
	for id, list := range w.m {
		for _, ch := range list {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		delete(w.m, id)
	}
	w.n = 0
}

// parked reports the number of currently parked waiters (metrics and tests).
func (w *waiters) parked() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}
