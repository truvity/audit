// Package cli is the implementation behind cmd/audit. It is internal because
// the command is the contract, not the Go API of its subcommands.
package cli

import (
	"fmt"
	"os"

	"github.com/truvity/audit/preset"
)

// LoadDeployment reads a deployment's profile configuration from a file.
func LoadDeployment(path string) (*preset.Deployment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := preset.ParseDeployment(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}
