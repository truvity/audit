package cli

import (
	"fmt"
	"os"

	"github.com/truvity/audit/internal/config"
	"github.com/truvity/audit/writer"
)

// LayerEnv is the variable a deployment on Lambda sets to the ARN of the layer
// version that carries the configuration. Nothing in the environment tells a
// function which layers it has, so the deployment that attached the layer says
// so, and the writer repeats it in its start-up record.
const LayerEnv = "AUDIT_CONFIG_LAYER"

// WriterEvidence is what the writer says about its configuration when it
// starts: the digest of the file it read, of the documents and the directory of
// catalogues that file names, and on Lambda the layer. A path that is empty is
// a document the configuration does not have, and is left out. A document that
// cannot be read is an error, because the configuration was about to fail to
// load it anyway and a record that omitted it would be a record that lied.
func WriterEvidence(src config.Source, deployment, workloads, catalogues string) (writer.Evidence, error) {
	e := writer.Evidence{
		ConfigFile: src.File, ConfigDigest: src.Digest,
		Layer: os.Getenv(LayerEnv), FunctionVersion: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
	}
	var err error
	if e.DeploymentDigest, err = config.DigestFile(deployment); err != nil {
		return e, fmt.Errorf("deployment: %w", err)
	}
	if e.WorkloadsDigest, err = config.DigestFile(workloads); err != nil {
		return e, fmt.Errorf("workloads: %w", err)
	}
	if e.CataloguesDigest, err = config.DigestTree(catalogues); err != nil {
		return e, fmt.Errorf("catalogues: %w", err)
	}
	return e, nil
}
