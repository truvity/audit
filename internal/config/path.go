package config

import (
	"errors"
	"os"
	"strings"

	policyconfig "github.com/truvity/policy/config"
)

// EnvConfig names the file a process is configured with, as an alternative to
// --config. The flag wins when both are given: it is the more specific, and
// a command line that names a file means that one.
const EnvConfig = "AUDIT_CONFIG"

// Path is the configuration file a process reads: truvity/policy's PathFrom
// (`--config` or `-config` on the command line, otherwise AUDIT_CONFIG), and,
// for a process that has a conventional place, the first of defaults that exists
// when neither is given: the Lambda binaries, whose layer mounts the file at
// /opt/audit/audit.yaml. A binary with no convention passes none, and refuses to
// start without the flag or the variable.
//
// args is the command line without the program's name, os.Args[1:].
func Path(args []string, schemaName string, defaults ...string) (string, error) {
	p, err := policyconfig.PathFrom(args, EnvConfig)
	if err == nil {
		return p, nil
	}
	// Only "nothing was named" falls back to a default; a flag with no value or
	// given twice is the person's mistake and is said.
	var pe *policyconfig.Error
	if errors.As(err, &pe) && strings.Contains(err.Error(), "no configuration file") {
		for _, d := range defaults {
			if _, statErr := os.Stat(d); statErr == nil {
				return d, nil
			}
		}
		if len(defaults) > 0 {
			return "", errors.New(err.Error() + ", or put it at " + strings.Join(defaults, " or ") +
				" (schemas/config/" + schemaName + ".schema.json says what it holds)")
		}
	}
	return "", errors.New(err.Error() + " (schemas/config/" + schemaName + ".schema.json says what it holds)")
}
