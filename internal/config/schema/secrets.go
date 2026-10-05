//nolint:lll // a schema is prose, and a description is one string
package schema

import (
	"bytes"
	"encoding/json"
	"strings"
)

// secretFields are the fields that name a secret in version 2, and the field of
// version 1 each replaced: the name of an environment variable (`...Env`) became
// the name of a secret (`...Secret`), which `secrets` says how to find.
var secretFields = map[string]string{
	"passwordSecret":    "passwordEnv",
	"credentialsSecret": "credentialsEnv",
	"tokenSecret":       "tokenEnv",
}

// bucketDef is truvity/policy's bucket shape with its credentials named as
// secrets. The fragment itself still says `credentialsEnv`, which version 2
// does not have, so the shape is taken from it and changed here.
func bucketDef() m {
	b := fragment("fragments/bucket.json")
	props := b["properties"].(map[string]any)
	creds := props["credentialsEnv"].(map[string]any)
	delete(props, "credentialsEnv")
	creds["description"] = "The NAMES of the secrets holding static credentials, never the values, resolved through `secrets`. Unset means the SDK's ambient credentials, which is what a workload identity provides."
	props["credentialsSecret"] = creds
	b["description"] = strings.ReplaceAll(b["description"].(string), "credentialsEnv", "credentialsSecret")
	return b
}

// postgresDef is truvity/policy's postgres shape with the password named as a
// secret, and with a password in the URL refused where the fragment's own
// pattern would let one through.
func postgresDef() m {
	p := fragment("fragments/postgres.json")
	props := p["properties"].(map[string]any)
	delete(props, "passwordEnv")
	props["passwordSecret"] = m{
		"type": "string", "minLength": 1,
		"description": "The NAME of the secret holding the password, resolved through `secrets`. Unset means the connection needs none.",
	}
	url := props["url"].(map[string]any)
	all := url["allOf"].([]any)
	url["allOf"] = append(all, m{"not": m{"pattern": `^[A-Za-z][A-Za-z0-9+.-]*://[^/?#@]*:[^/?#@]*@`}})
	url["description"] = "A connection URL without credentials, for example postgres://user@host:5432/dbname?sslmode=require. A password in the user information or in a password or sslpassword parameter is refused: the file is rendered into objects people read, and the password belongs to `passwordSecret`."
	p["description"] = "A PostgreSQL connection. The URL carries no password: a password in it is refused, and `passwordSecret` names the secret that holds it."
	return p
}

// withSecrets adds the `secrets` property to a schema that has a field naming a
// secret anywhere in it.
func withSecrets(s m) {
	b, _ := json.Marshal(s)
	for f := range secretFields {
		if bytes.Contains(b, []byte(`"`+f+`"`)) {
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
