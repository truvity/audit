package preset

import (
	"fmt"
	"slices"
	"strings"
)

// CheckPresetsLock holds a deployment's lock mode to the built-in presets a
// profile is composed from: a preset whose framework demands an Object Lock
// stricter than the deployment's refuses the composition, as CheckLockMode
// does for the composed profile. The mode is spelled either way (NONE or none),
// so a Pulumi stack's NONE/GOVERNANCE/COMPLIANCE can be passed as it is.
//
// It lets a deployment's own configuration be refused before anything is
// built, without composing a whole profile document.
func CheckPresetsLock(names []string, deployment string) error {
	mode := strings.ToLower(deployment)
	if LockRank(mode) < 0 {
		return fmt.Errorf("lock mode %q is not one of none, governance or compliance", deployment)
	}

	builtin, err := Builtin()
	if err != nil {
		return err
	}

	var weaker []string

	for _, name := range names {
		p, ok := builtin[name]
		if !ok {
			return fmt.Errorf("preset %q is not one of the built-in presets", name)
		}

		if LockRank(p.Integrity.ObjectLockMode) > LockRank(mode) {
			weaker = append(weaker, name)
		}
	}

	if len(weaker) > 0 {
		return fmt.Errorf("preset(s) %s may demand an Object Lock stricter than %s, which this deployment has (presets that demand none: %s)",
			strings.Join(weaker, ", "), mode, strings.Join(LockFreePresets(builtin, mode), ", "))
	}

	return nil
}

// LockFreePresets are the names of the presets whose frameworks demand no
// stricter a lock than mode, sorted.
func LockFreePresets(presets map[string]*Preset, mode string) []string {
	var out []string

	for name, p := range presets {
		if LockRank(p.Integrity.ObjectLockMode) <= LockRank(mode) {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return out
}
