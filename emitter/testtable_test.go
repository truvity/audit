package emitter

import "testing"

// openTable is a table whose schemas accept any object, for the tests of the emitter's own rules (bounds, the e-mail
// shape, the note of lost facts, the transport): they probe the mechanics with events that a closed schema would refuse.
// What a closed schema refuses is in table_test.go, with closedTable.
func openTable(t testing.TB) *Table {
	t.Helper()
	tb, err := NewTable(
		[]Row{{Name: "key.sign", Component: "key", Class: 6003, Activities: []Activity{{ID: 99, Name: "Sign"}}, Outcomes: []Status{StatusSuccess, StatusFailure}}},
		map[string]string{DefsName: `{}`, "key.sign.schema.json": `{"type":"object"}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

// closedTable is a table of one action whose product object is closed, in the shape a generator makes: purpose, key_id and
// caller are required words, nothing else may be in it, and the note of lost facts is allowed. The Rust tests use the same
// documents.
func closedTable(t testing.TB) *Table {
	t.Helper()
	tb, err := NewTable(
		[]Row{{Name: "key.sign", Component: "key", Class: 6003, Activities: []Activity{{ID: 99, Name: "Sign"}}, Outcomes: []Status{StatusSuccess}}},
		map[string]string{
			DefsName:               `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"word":{"type":"string","pattern":"^[a-z0-9][a-z0-9_.-]{0,63}$"},"lost":{"type":"object","additionalProperties":false,"required":["process","since","total"],"properties":{"process":{"type":"string","pattern":"^[0-9a-f]{16}$"},"since":{"type":"integer","minimum":0,"maximum":9007199254740991},"total":{"type":"integer","minimum":1,"maximum":9007199254740991}}}}}`,
			"key.sign.schema.json": `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["metadata","unmapped"],"properties":{"metadata":{"type":"object","properties":{"event_code":{"const":"key.sign"},"product":{"type":"object","properties":{"name":{"const":"example"}}}}},"unmapped":{"type":"object","additionalProperties":false,"required":["example"],"properties":{"example":{"type":"object","additionalProperties":false,"required":["purpose","key_id","caller"],"properties":{"purpose":{"$ref":"defs.schema.json#/$defs/word"},"key_id":{"$ref":"defs.schema.json#/$defs/word"},"caller":{"$ref":"defs.schema.json#/$defs/word"},"lost":{"$ref":"defs.schema.json#/$defs/lost"}}}}}}}`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

// authTable is a table whose schemas accept any object, with one Authentication action, for the tests of a record that
// names no subject: OCSF requires the user of an Authentication, and the record says unknown.
func authTable(t testing.TB) *Table {
	t.Helper()
	tb, err := NewTable(
		[]Row{{Name: "session.signin", Component: "session", Class: 3002, Activities: []Activity{{ID: 1}}, Outcomes: []Status{StatusSuccess, StatusFailure}}},
		map[string]string{DefsName: `{}`, "session.signin.schema.json": `{"type":"object"}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}
