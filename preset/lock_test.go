package preset

import "testing"

func TestCheckPresetsLock(t *testing.T) {
	if err := CheckPresetsLock([]string{"security", "history", "billing-nl"}, "NONE"); err != nil {
		t.Fatalf("lock-free presets on a NONE bucket: %v", err)
	}

	if err := CheckPresetsLock([]string{"security", "dora"}, "NONE"); err == nil {
		t.Fatal("dora demands compliance: want a refusal on NONE")
	}

	if err := CheckPresetsLock([]string{"dora"}, "COMPLIANCE"); err != nil {
		t.Fatalf("dora on COMPLIANCE: %v", err)
	}

	if err := CheckPresetsLock([]string{"nope"}, "none"); err == nil {
		t.Fatal("an unknown preset is refused")
	}

	if err := CheckPresetsLock([]string{"security"}, "weird"); err == nil {
		t.Fatal("an unknown mode is refused")
	}
}
