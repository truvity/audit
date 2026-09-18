package catalogue

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The viewer finds a template's arguments with its own scanner, because ICU
// forbids the dots in them and it has to rename them before parsing. Both
// scanners are held to one fixture, so a template cannot mean one thing to the
// validator and another to the viewer.
func TestMessageArgumentsAgreeWithTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Template  string   `json:"template"`
		Arguments []string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := MessageArguments(c.Template)
		if strings.Join(got, "|") != strings.Join(c.Arguments, "|") {
			t.Errorf("%q: got %q, want %q", c.Template, got, c.Arguments)
		}
	}
}
