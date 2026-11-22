#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! The action table: what it refuses, with an example table of one action, and the policy of the schema documents of a
//! catalogue against the shared vectors of `vectors/` that the Go package reads too. A product tests its own table with its
//! own vectors.

use std::sync::Arc;

use serde_json::Value;

use crate::table::{Activity, DEFS_NAME, Row, Table};
use crate::tests::{arr, parse, refusal_of, text};
use crate::{Action, Event, Status};

/// The rows of the example table: one action of API Activity.
static ROWS: &[Row] = &[Row {
    name: "key.sign",
    component: "key",
    class: 6003,
    activities: &[Activity {
        id: 99,
        name: "Sign",
    }],
    outcomes: &[1],
}];

/// The schemas of the example table, in the shape a generator makes: the record's product object is closed, and the note
/// of lost facts is allowed in it.
static SCHEMAS: &[(&str, &str)] = &[
    (
        DEFS_NAME,
        r#"{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"word":{"type":"string","pattern":"^[a-z][a-z0-9_.-]{0,63}$"},"lost":{"type":"object","additionalProperties":false,"required":["process","since","total"],"properties":{"process":{"type":"string","pattern":"^[0-9a-f]{16}$"},"since":{"type":"integer","minimum":0,"maximum":9007199254740991},"total":{"type":"integer","minimum":1,"maximum":9007199254740991}}}}}"#,
    ),
    (
        "key.sign.schema.json",
        r#"{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["metadata"],"properties":{"metadata":{"type":"object","required":["event_code"],"properties":{"event_code":{"const":"key.sign"}}},"unmapped":{"type":"object","additionalProperties":false,"properties":{"example":{"type":"object","additionalProperties":false,"properties":{"key_id":{"$ref":"defs.schema.json#/$defs/word"},"lost":{"$ref":"defs.schema.json#/$defs/lost"}}}}}}}"#,
    ),
];

pub(crate) fn example_table() -> Arc<Table> {
    Arc::new(Table::new(ROWS, SCHEMAS).unwrap())
}

fn sign() -> Event {
    Event {
        caller: "signer".into(),
        operation: "Sign".into(),
        action: Action {
            name: "key.sign".into(),
            class_uid: 6003,
            activity_id: 99,
            activity_name: "Sign".into(),
        },
        status: Status::Success,
        ..Event::default()
    }
}

/// verifies: REQ-AUDIT-022
#[test]
fn the_table_refuses_an_action_that_is_not_one_of_its_rows() {
    let t = example_table();
    t.check_action(&sign().action).unwrap();
    for (name, a) in [
        (
            "a name that is no row",
            Action {
                name: "key.open".into(),
                activity_name: "Open".into(),
                ..sign().action
            },
        ),
        (
            "another class",
            Action {
                class_uid: 3004,
                ..sign().action
            },
        ),
        (
            "another activity",
            Action {
                activity_id: 1,
                activity_name: String::new(),
                ..sign().action
            },
        ),
        (
            "another activity name",
            Action {
                activity_name: "Open".into(),
                ..sign().action
            },
        ),
    ] {
        let e = t.check_action(&a).expect_err(name);
        assert_eq!(refusal_of(&e).0, "schema", "{name}");
    }
}

/// verifies: REQ-AUDIT-099
#[test]
fn the_table_refuses_a_record_whose_member_is_not_in_the_row_and_names_no_value() {
    let t = example_table();
    let good = serde_json::json!({
        "metadata": {"event_code": "key.sign"},
        "unmapped": {"example": {"key_id": "k1"}},
    });
    t.validate(good.to_string().as_bytes()).unwrap();
    let mut rec = good.clone();
    rec["unmapped"] = serde_json::json!({"example": {"a_secret_value": "x"}});
    let e = t.validate(rec.to_string().as_bytes()).unwrap_err();
    let (reason, path) = refusal_of(&e);
    assert_eq!(reason, "schema");
    assert!(path.starts_with("record"), "{path}");
    assert!(!e.to_string().contains("\"x\""), "{e}");
    let mut other = good;
    other["unmapped"] = serde_json::json!({"other": {"key_id": "k1"}});
    let e = t.validate(other.to_string().as_bytes()).unwrap_err();
    assert_eq!(refusal_of(&e).0, "schema");
}

/// A table refuses an action that is twice among its rows and an action with no schema.
#[test]
fn a_table_needs_one_row_and_one_schema_for_each_action() {
    static TWICE: &[Row] = &[ROWS[0], ROWS[0]];
    assert!(Table::new(TWICE, SCHEMAS).is_err());
    static NO_SCHEMA: &[(&str, &str)] = &[(DEFS_NAME, "{}")];
    assert!(Table::new(ROWS, NO_SCHEMA).is_err());
    static NO_DEFS: &[(&str, &str)] = &[("key.sign.schema.json", "{}")];
    assert!(Table::new(ROWS, NO_DEFS).is_err());
}

/// verifies: REQ-AUDIT-022
///
/// The emitter refuses an unknown action as an action, before it builds a record, and not as a record that fails its
/// schema: the path of the refusal is `action`.
#[tokio::test]
async fn the_emitter_refuses_an_unknown_action_by_name() {
    let e = crate::Emitter::new(crate::Config {
        product: "example".into(),
        release: "0.0.0-test".into(),
        scope: "dev".into(),
        component: "key".into(),
        table: example_table(),
        transport_socket: "/nonexistent/emit.sock".into(),
        pseudonyms: None,
        correlation: None,
        now: None,
    })
    .unwrap();
    let mut ev = sign();
    ev.action.name = "key.open".into();
    let err = e.record(ev).await.unwrap_err();
    let (reason, path) = refusal_of(&err);
    assert_eq!((reason.as_str(), path.as_str()), ("schema", "action"));
}

const POLICY: &str = include_str!("../vectors/schema-policy-vectors.json");
const SEMVER: &str = include_str!("../vectors/semver-vectors.json");

/// verifies: REQ-AUDIT-149
///
/// The policy of the schema documents of an `AuditCatalogue`, on the corpus the Go package reads too: the closed keywords, the
/// references to the shared definitions only, patterns RE2 and Rust read alike, bounded depth and size. A document the policy
/// takes also compiles.
#[test]
fn the_policy_of_the_catalogue_schemas_gives_the_verdicts_of_the_shared_vectors() {
    let f = parse(POLICY);
    assert!(
        arr(&f, "cases").len() >= 50,
        "the vector file lost its cases"
    );
    for c in arr(&f, "cases") {
        let docs: Vec<(String, String)> = c["docs"]
            .as_object()
            .unwrap()
            .iter()
            .map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned()))
            .collect();
        let ok = c["ok"].as_bool().unwrap();
        let verdict = crate::check_documents(&docs);
        assert_eq!(verdict.is_ok(), ok, "{}: {verdict:?}", text(c, "name"));
        if ok {
            assert!(crate::check_policy(&docs).is_ok(), "{}", text(c, "name"));
        }
    }
}

/// verifies: REQ-AUDIT-149
#[test]
fn the_releases_of_the_shared_vectors() {
    let f = parse(SEMVER);
    assert!(arr(&f, "accept").len() >= 10 && arr(&f, "reject").len() >= 20);
    for s in arr(&f, "accept") {
        assert!(crate::valid_release(s.as_str().unwrap()), "{s} refused");
    }
    for s in arr(&f, "reject") {
        assert!(!crate::valid_release(s.as_str().unwrap()), "{s} taken");
    }
}

/// verifies: REQ-AUDIT-149
///
/// The patterns the policy takes mean the same in both engines: each accepted pattern matches and refuses the strings of the
/// corpus, here through the engine the Rust library uses (and the `regex` crate), in Go through RE2.
#[test]
fn accepted_patterns_match_as_the_corpus_says() {
    let f = parse(POLICY);
    assert!(
        arr(&f, "matches").len() >= 8,
        "the vector file lost its matches"
    );
    for m in arr(&f, "matches") {
        let p = text(m, "pattern");
        let docs = vec![
            (
                crate::DEFS_NAME.to_owned(),
                r#"{"$schema":"https://json-schema.org/draft/2020-12/schema"}"#.to_owned(),
            ),
            (
                "x.a.schema.json".to_owned(),
                serde_json::json!({"$schema":"https://json-schema.org/draft/2020-12/schema","properties":{"a":{"type":"string","pattern":p}}}).to_string(),
            ),
        ];
        crate::check_documents(&docs).unwrap_or_else(|e| panic!("{p}: refused by the policy: {e}"));
        let v =
            jsonschema::validator_for(&serde_json::json!({"type":"string","pattern":p})).unwrap();
        let re = regex::Regex::new(p).unwrap();
        for (key, want) in [("yes", true), ("no", false)] {
            for s in arr(m, key) {
                let s = s.as_str().unwrap();
                assert_eq!(
                    v.is_valid(&serde_json::json!(s)),
                    want,
                    "{p} on {s:?} (the library)"
                );
                assert_eq!(re.is_match(s), want, "{p} on {s:?} (the regex crate)");
            }
        }
    }
}

fn corpus_docs(c: &Value) -> Vec<(String, String)> {
    c["docs"]
        .as_object()
        .unwrap()
        .iter()
        .map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned()))
        .collect()
}

/// verifies: REQ-AUDIT-149
///
/// The policy is linear: every case of the corpus is checked in well under a second, the pathological ones included (a check
/// that walked every duplicate reference did not finish in a minute).
#[test]
fn the_policy_is_fast_on_every_case_of_the_corpus() {
    let f = parse(POLICY);
    let start = std::time::Instant::now();
    for c in arr(&f, "cases") {
        let t0 = std::time::Instant::now();
        let _ = crate::check_policy(&corpus_docs(c));
        assert!(
            t0.elapsed() < std::time::Duration::from_millis(500),
            "{}: {:?}",
            text(c, "name"),
            t0.elapsed()
        );
    }
    assert!(
        start.elapsed() < std::time::Duration::from_secs(3),
        "{:?}",
        start.elapsed()
    );
}
