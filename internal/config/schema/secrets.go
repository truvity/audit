package schema

import (
	"bytes"
	"encoding/json"
)

// secretMarkers are what, in a schema, means a field names a secret: a field
// of this repository's own (`tokenSecret`), or a reference to a shared shape of
// truvity/policy that has one (`passwordSecret`, `credentialsSecret`).
var secretMarkers = []string{`"tokenSecret"`, "fragments/postgres.json", "fragments/bucket.json"}

// withSecrets adds the `secrets` property to a schema that has a field naming a
// secret anywhere in it.
func withSecrets(s m) {
	b, _ := json.Marshal(s)
	for _, f := range secretMarkers {
		if bytes.Contains(b, []byte(f)) {
			s["properties"].(m)["secrets"] = def("secrets")
			if defs, ok := s["$defs"].(m); ok {
				defs["secrets"] = sharedDefs()["secrets"]
			} else {
				s["$defs"] = m{"secrets": sharedDefs()["secrets"]}
			}
			return
		}
	}
}
