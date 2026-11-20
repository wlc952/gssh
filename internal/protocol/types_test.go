package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type retryableTestError struct{}

func (retryableTestError) Error() string   { return "temporary" }
func (retryableTestError) Retryable() bool { return true }

func TestErrorPayloadStructuredAndBackwardCompatible(t *testing.T) {
	p := NewErrorPayload(retryableTestError{}, "prod", "task-1")
	if p.Code != -32000 || !p.Retryable || p.Session != "prod" || p.TaskID != "task-1" {
		t.Fatalf("unexpected payload: %#v", p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out ErrorPayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, p) {
		t.Fatalf("round trip mismatch: %#v != %#v", out, p)
	}
}

func TestErrorPayloadDeadlineIsRetryable(t *testing.T) {
	if !NewErrorPayload(context.DeadlineExceeded, "", "").Retryable {
		t.Fatal("deadline errors should be marked retryable")
	}
}

func TestEncodeDecodePayloadRoundTrip(t *testing.T) {
	in := ExecParams{
		Name:    "prod",
		Command: "printf '%s' hello",
		Timeout: 15,
		Sudo:    SudoOptions{Enabled: true, User: "root", Login: true},
	}
	b, err := EncodePayload(in)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	var out ExecParams
	if err := DecodePayload(b, &out); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", out, in)
	}
}

func TestDecodePayloadRejectsMalformedJSON(t *testing.T) {
	var out ConnectParams
	if err := DecodePayload([]byte(`{"host":`), &out); err == nil {
		t.Fatal("DecodePayload accepted malformed JSON")
	}
}

func TestStreamChunkPreservesBinaryData(t *testing.T) {
	in := StreamChunk{Stream: "stdout", Data: []byte{0, 1, 2, 0xff}}
	b, err := EncodePayload(in)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	var out StreamChunk
	if err := DecodePayload(b, &out); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if out.Stream != in.Stream || !bytes.Equal(out.Data, in.Data) {
		t.Fatalf("binary round trip mismatch: got %#v, want %#v", out, in)
	}
}

func TestEncodePayloadUnsupportedValue(t *testing.T) {
	if _, err := EncodePayload(func() {}); err == nil {
		t.Fatal("EncodePayload accepted unsupported function value")
	}
}
