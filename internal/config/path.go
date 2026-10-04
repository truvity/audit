package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// EnvConfig names the file a process is configured with, as an alternative to
// --config. The flag wins when both are given: it is the more specific, and
// a command line that names a file means that one.
//
// TODO(policy): truvity/policy is adding config.PathFrom and an apiVersion
// envelope. When that is released, Path becomes a call to it and this file
// goes; the rules below are the ones it is to keep, so that no binary's
// behaviour changes.
const EnvConfig = "AUDIT_CONFIG"

// Path is the configuration file a process reads: the --config flag when it is
// set, otherwise AUDIT_CONFIG, otherwise the first of defaults that exists.
// With none of them it is an error that says how to give one, so that a
// process never starts on a file nobody named.
//
// defaults is for a process that has a conventional place: the Lambda
// binaries, whose layer mounts the file at /opt/audit/audit.yaml. A binary with
// no convention passes none, and refuses to start without the flag or the
// variable.
func Path(flagValue, schemaName string, defaults ...string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvConfig)); v != "" {
		return v, nil
	}
	for _, d := range defaults {
		if _, err := os.Stat(d); err == nil {
			return d, nil
		}
	}
	hint := "give the configuration file with --config or " + EnvConfig
	if len(defaults) > 0 {
		hint += ", or put it at " + strings.Join(defaults, " or ")
	}
	return "", errors.New(hint + ": it is the only thing that configures this process " +
		fmt.Sprintf("(schemas/config/%s.schema.json says what it holds)", schemaName))
}
