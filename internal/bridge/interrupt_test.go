package bridge

import (
	"context"
	"slices"
	"testing"

	"github.com/coder/websocket"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	stat "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/request_stat"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"

	"aiboxy/internal/wire"
)

func playbackStat(id string, finished bool) []byte {
	ts := &stat.TRequestStat_TTimestamps{}
	if finished {
		ts.OnSoundPlayerEndTime = new("1700000000000")
	}
	return aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "stat-" + id}, Event: &client.TClientMessage_TEvent_RequestStat{RequestStat: &stat.TRequestStat{RequestMessageId: id, Timestamps: ts}}}}})
}

// answer sends a request and waits for its local answer to be synthesized,
// returning the TTS request id and the requests to cancel sent on the way.
func answer(t *testing.T, n *Native, up chan wire.Frame, id string) (string, []string) {
	t.Helper()
	if err := n.Client(t.Context(), websocket.MessageBinary, soundInput(id)); err != nil {
		t.Fatal(err)
	}
	var canceled []string
	for {
		e := nextSoundFrame(t, up).Client.GetEvent()
		if c := e.GetCancelRequest(); c != nil {
			canceled = append(canceled, c.GetRequestMessageId())
		}
		if e.GetTtsGenerate() != nil {
			return e.GetHeader().GetMessageId(), canceled
		}
	}
}

func TestNativeInterruptedAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stat     bool // the device reports playback before the next request
		finished bool
		want     bool
	}{
		{"cut by wake word", true, false, true},
		{"next request before any report", false, false, true},
		{"played to the end", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan bool, 4)
			n, up, _ := soundSession(t, resolverFunc(func(ctx context.Context, _ string) (Resolution, error) {
				seen <- Interrupted(ctx)
				return Resolution{Route: "llm", Answer: "Длинный ответ."}, nil
			}))
			ttsID, _ := answer(t, n, up, "first")
			<-seen
			if tc.stat {
				if err := n.Client(t.Context(), websocket.MessageBinary, playbackStat("first", tc.finished)); err != nil {
					t.Fatal(err)
				}
			}
			_, cancels := answer(t, n, up, "second")
			if got := <-seen; got != tc.want {
				t.Fatalf("interrupted = %v, want %v", got, tc.want)
			}
			canceled := slices.Contains(cancels, ttsID)
			n.mu.Lock()
			first := n.requests["first"]
			n.mu.Unlock()
			if tc.want && (!first.canceled || !canceled) {
				t.Fatalf("cut answer still live: canceled=%v tts_cancel_sent=%v", first.canceled, canceled)
			}
		})
	}
}

func TestNativeSilentAnswerKeepsListening(t *testing.T) {
	n, up, down := soundSession(t, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "llm", Silent: true}, nil
	}))
	if err := n.Client(t.Context(), websocket.MessageBinary, soundInput("stop")); err != nil {
		t.Fatal(err)
	}
	for {
		r := nextSoundFrame(t, down).Server.GetEvent().GetAliceResponse()
		if r == nil {
			continue
		}
		if r.GetVoiceResponse().GetHasVoiceResponse() || r.GetVoiceResponse().GetOutputSpeech().GetText() != "" || !r.GetVoiceResponse().GetShouldListen() {
			t.Fatalf("silent answer: %+v", r.GetVoiceResponse())
		}
		break
	}
	for len(up) > 0 {
		if (<-up).Client.GetEvent().GetTtsGenerate() != nil {
			t.Fatal("silent answer was synthesized")
		}
	}
}
