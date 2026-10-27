package wire

import (
	"testing"

	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
)

func TestDisableAntimatSetsOnlyThatFlag(t *testing.T) {
	lang := "ru-RU"
	m := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "id-1"},
		Event:  &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Lang: &lang}},
	}}}
	raw, err := (Frame{Client: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	patched, err := DisableAntimat(Decode(raw, ClientToServer))
	if err != nil {
		t.Fatal(err)
	}
	f := Decode(patched, ClientToServer)
	req := f.Client.GetEvent().GetVoiceInput()
	if req == nil || !req.GetDisableAntimat() || req.GetLang() != "ru-RU" || f.Client.GetEvent().GetHeader().GetMessageId() != "id-1" {
		t.Fatalf("patched request = %v", req)
	}
	if string(patched[:4]) != "AAPI" {
		t.Fatal("transport prefix lost")
	}
}
