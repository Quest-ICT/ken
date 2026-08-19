package commserver

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

func TestZZSweepProbe(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"comm.ErrNotFound", comm.ErrNotFound},
		{"comm.ErrDenied", comm.ErrDenied},
		{"comm.ErrBackpressure", comm.ErrBackpressure},
		{"comm.ErrTooLarge", comm.ErrTooLarge},
		{"comm.ErrChannelClosed", comm.ErrChannelClosed},
		{"comm.ErrFilesDisabled", comm.ErrFilesDisabled},
		{"comm.ErrQuota", comm.ErrQuota},
		{"comm.ErrBadName", comm.ErrBadName},
		{"comm.ErrShortWrite", comm.ErrShortWrite},
		{"comm.ErrNotAStation", comm.ErrNotAStation},
		{"comm.ErrNotLinked", comm.ErrNotLinked},
		{"comm.ErrSelfSend", comm.ErrSelfSend},
		{"comm.ErrUnknownStation", comm.ErrUnknownStation},
		{"comm.ErrRoomEmpty", comm.ErrRoomEmpty},
		{"comm.ErrNotInRoom", comm.ErrNotInRoom},
		{"comm.ErrNoAudience", comm.ErrNoAudience},
		{"store.ErrStationKeyRevoked", store.ErrStationKeyRevoked},
		{"store.ErrStationArchived", store.ErrStationArchived},
		{"inline: sha256 must be 64 hex digits", errors.New("sha256 must be 64 hex digits")},
		{"inline: path transfer requires nonce", errors.New("path transfer requires nonce_sha256 (64 hex digits) — see the rendezvous protocol")},
		{"inline: transfer must be path or upload", errors.New(`transfer must be "path" or "upload"`)},
		{"inline: attachment has no downloadable bytes", errors.New("attachment has no downloadable bytes")},
		{"inline: attachment is not awaiting an upload", errors.New("attachment is not awaiting an upload")},
		{"inline: unknown scope kind", errors.New("unknown scope kind: c:x")},
		{"CallerSafe ChannelFor room-as-channel", comm.CallerSafe(fmt.Errorf("%w: %q is a ROOM, not a channel", comm.ErrNotFound, "r1"))},
		{"wrapped-not-safe (deliver to X)", fmt.Errorf("deliver to %s: %w", "s:a", comm.ErrBackpressure)},
	}
	for _, c := range cases {
		got := commError(c.err)
		intact := got.Error() == c.err.Error()
		t.Logf("%-46s intact=%-5v  in=%.55q\n%50sout=%.120q", c.name, intact, c.err.Error(), "", got.Error())
	}
}
