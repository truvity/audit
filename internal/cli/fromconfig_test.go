package cli

import (
	"strings"
	"testing"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/sink"
)

func TestSinkFromMapsExpectAndGuardsRequire(t *testing.T) {
	c := config.Sink{URL: "http://audit:8080", Expect: "queued"}

	got, err := SinkFrom(c, "")
	if err != nil || sink.Guarantees(got) != sink.Queued {
		t.Fatalf("expect did not become Expecting: %v %v", err, got)
	}
	got, err = SinkFrom(c, "queued")
	if err != nil || sink.Guarantees(got) != sink.Queued {
		t.Fatalf("a chain that meets require: %v", err)
	}
	if _, err = SinkFrom(c, "archived"); err == nil || !strings.Contains(err.Error(), "queued at best") {
		t.Errorf("expect queued under require archived was accepted: %v", err)
	}
	if _, err = SinkFrom(config.Sink{URL: "http://a"}, "logged"); err == nil || !strings.Contains(err.Error(), "sink.expect") {
		t.Errorf("a require with no expect must say to set sink.expect: %v", err)
	}
	if _, err = SinkFrom(config.Sink{URL: "http://a", Expect: "durable"}, ""); err == nil {
		t.Error("an unknown expect was accepted")
	}
	// No require, no expect: the client is as it was, and says nothing.
	plain, err := SinkFrom(config.Sink{URL: "http://a"}, "")
	if err != nil || sink.Guarantees(plain) != sink.Unspecified {
		t.Errorf("the plain client changed: %v", err)
	}
}
