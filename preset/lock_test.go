package preset

import (
	"strings"
	"testing"
)

// A profile's lock mode is the LEAST the store must run, composed towards the
// stricter reading: a preset that demands no lock composed with one that
// demands compliance is a compliance profile, and none is below governance.
func TestLockModesComposeTowardsTheStricter(t *testing.T) {
	for _, tc := range []struct{ a, b, want string }{
		{"none", "governance", "governance"},
		{"governance", "none", "governance"},
		{"none", "compliance", "compliance"},
		{"compliance", "none", "compliance"},
		{"governance", "compliance", "compliance"},
		{"none", "none", "none"},
		{"", "none", "none"},
		{"none", "", "none"},
	} {
		got := stricterIntegrity(Integrity{ObjectLockMode: tc.a}, Integrity{ObjectLockMode: tc.b}).ObjectLockMode
		if got != tc.want {
			t.Errorf("stricter(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// The note that survives composition is the one behind the mode that won,
// so that a profile explains the lock it ended up with and not the reading it
// discarded.
func TestTheWinningLockKeepsItsNote(t *testing.T) {
	loose := Integrity{ObjectLockMode: "none", Note: "optional here"}
	strict := Integrity{ObjectLockMode: "compliance", Note: "the assessor expects WORM"}
	if got := stricterIntegrity(loose, strict).Note; got != strict.Note {
		t.Fatalf("note = %q, want the stricter preset's", got)
	}
	if got := stricterIntegrity(strict, loose).Note; got != strict.Note {
		t.Fatalf("note = %q, want the stricter preset's whichever order", got)
	}
}

// The presets this repository ships demand the lock only where the framework
// does. Somebody editing one of these files should have to edit this test too.
func TestShippedPresetsDemandTheLockOnlyWhereTheFrameworkDoes(t *testing.T) {
	presets, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"pci-dss": LockCompliance, "nen-7513": LockCompliance, "dora": LockCompliance, "evidence-etsi": LockCompliance,
		"security": LockNone, "history": LockNone, "billing-nl": LockNone,
	}
	for name, mode := range want {
		p, ok := presets[name]
		if !ok {
			t.Fatalf("no preset %s", name)
		}
		if p.Integrity.ObjectLockMode != mode {
			t.Errorf("%s: object_lock_mode = %q, want %q", name, p.Integrity.ObjectLockMode, mode)
		}
		if strings.TrimSpace(p.Integrity.Note) == "" {
			t.Errorf("%s: a lock reading with no note is one a reviewer has to reconstruct", name)
		}
	}
	if len(presets) != len(want) {
		t.Errorf("%d presets shipped, %d named here: name the new one", len(presets), len(want))
	}
}

// The deployment's lock mode must be at least what every profile demands. A
// stricter store is fine; a weaker one is refused, naming the profile and
// both modes, so that the operator reads the reason rather than the S3 error
// that would otherwise arrive one object at a time.
func TestCheckLockModeRefusesAWeakerStore(t *testing.T) {
	profiles := map[string]*Profile{
		"security": {Name: "security", Integrity: Integrity{ObjectLockMode: LockNone}},
		"payments": {Name: "payments", Integrity: Integrity{ObjectLockMode: LockCompliance}},
		"staging":  {Name: "staging", Integrity: Integrity{ObjectLockMode: LockGovernance}},
	}
	if err := CheckLockMode(profiles, LockCompliance); err != nil {
		t.Fatalf("a compliance store satisfies every profile, got %v", err)
	}
	err := CheckLockMode(profiles, LockGovernance)
	if err == nil {
		t.Fatal("a compliance profile on a governance store was allowed")
	}
	for _, want := range []string{"profile payments", "compliance mode", "lock mode governance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "profile staging") || strings.Contains(err.Error(), "profile security") {
		t.Errorf("a profile the store satisfies was named: %v", err)
	}
	err = CheckLockMode(profiles, LockNone)
	if err == nil {
		t.Fatal("locked profiles on an unlocked store were allowed")
	}
	if !strings.Contains(err.Error(), "profile payments") || !strings.Contains(err.Error(), "profile staging") {
		t.Errorf("both profiles the store fails should be named: %v", err)
	}

	only := map[string]*Profile{"security": profiles["security"]}
	if err := CheckLockMode(only, LockNone); err != nil {
		t.Fatalf("a profile that demands no lock runs on an unlocked store, got %v", err)
	}
	if err := CheckLockMode(only, "sideways"); err == nil {
		t.Fatal("an unknown deployment mode was accepted")
	}
}
