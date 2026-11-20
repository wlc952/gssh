package main

import (
	"encoding/json"
	"testing"
)

func TestUsageJSONAdvertisesCapabilities(t *testing.T) {
	b, err := usageJSON()
	if err != nil {
		t.Fatalf("usageJSON: %v", err)
	}
	var doc helpDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("usageJSON is not valid JSON: %v", err)
	}
	if doc.Name != "gssh" || len(doc.Commands) == 0 {
		t.Fatalf("unexpected help document: %+v", doc)
	}
	if doc.Capabilities["interactive_tty"] {
		t.Fatal("interactive_tty must remain disabled until an explicit TTY protocol exists")
	}
	for _, capability := range []string{"streaming_exec", "batch_exec", "sftp_resume", "proxy_jump"} {
		if !doc.Capabilities[capability] {
			t.Errorf("capability %q not advertised", capability)
		}
	}
}
