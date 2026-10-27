package bridge

import (
	"context"
	"log/slog"
	"time"
)

// cutShortWindow is how long after the device stops an answer early the next
// request still counts as the one that interrupted it.
const cutShortWindow = 5 * time.Second

type interruptedKey struct{}

// Interrupted reports whether the request was spoken over a local answer
// that was still playing.
func Interrupted(ctx context.Context) bool {
	v, _ := ctx.Value(interruptedKey{}).(bool)
	return v
}

// WithInterrupted marks a request as spoken over a playing answer.
func WithInterrupted(ctx context.Context) context.Context {
	return context.WithValue(ctx, interruptedKey{}, true)
}

// playbackDoneLocked records the device's playback report for the speaking
// answer. The device reports an answer the wake word cut off as unfinished,
// just before it sends the request that interrupted it.
func (n *Native) playbackDoneLocked(id string, finished bool) {
	p := n.speaking
	if p == nil || p.id != id {
		return
	}
	n.speaking = nil
	if !finished {
		n.cutShort, n.cutAt = p, time.Now()
	}
}

// interruptLocked tells whether a new request interrupts a local answer and
// stops what is left of that answer, so its remaining audio is dropped. It
// returns the TTS request to cancel upstream, or "" when nothing was cut.
func (n *Native) interruptLocked() (string, bool) {
	p := n.speaking
	if p == nil && n.cutShort != nil && time.Since(n.cutAt) < cutShortWindow {
		p = n.cutShort
	}
	n.speaking, n.cutShort = nil, nil
	if p == nil {
		return "", false
	}
	slog.Info("native_answer_interrupted", "request", requestTag(p.id))
	if p.canceled {
		return "", true
	}
	p.canceled = true
	p.cancel()
	return p.ttsID, true
}
