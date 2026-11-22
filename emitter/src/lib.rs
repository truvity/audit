//! The audit emitter (record 0002, "Five pieces"): a library inside each emitting process. It holds
//! no credential, and from one call it builds one message, an OCSF 1.9.0 security record as RFC
//! 8785 canonical JSON (record 0004, "The record format"), checked against the product's action
//! table, and hands it to atoom's transport port.
//!
//! The emitter speaks audit's neutral model (record 0003): a product, its components, a scope and
//! a tenant. A product configures it with its own names ([`Config`]), its action table
//! ([`Table`]) and, for records that name a person, a [`Mac`] of the `pseudonym` purpose. The
//! product's own facts go into one object, `unmapped.<product>`.
//!
//! A person is never named in a record. The emitter asks the [`Mac`] under the tenant's
//! `pseudonym` key before the record leaves the process (record 0011, "Pseudonyms"), so a
//! [`Subject`] becomes `p1.<YYYYMM>.<first 128 bits of the MAC>`.
//!
//! [`Emitter::emit`] hands the message over and returns; [`Emitter::emit_blocking`] returns only
//! when the transport says the message is durable (record 0002, "The transport"). Delivery is at
//! least once and the writer removes a repeat by `metadata.uid`. The library keeps no queue and
//! does not retry: the transport driver behind the socket is the durable buffer, and a failure is
//! returned to the caller, who logs it.
//!
//! A best-effort fact that cannot be delivered ([`Emitter::emit`], or
//! [`Emitter::emit_blocking_best_effort`]) is lost, and the emitter counts it in memory, for the
//! tenant of the event. A record of that tenant carries the running total of the process as
//! `unmapped.<product>.lost` (`process`, `total`, `since`) until a record that carries the current
//! total has been delivered, so a repeat is harmless and a reader takes the highest total of a
//! process and never sums (record 0005, "Loss is counted while the process lives"). The total is
//! not a queue and nothing is resent; a process that exits with an unreported total takes it with
//! it. A call that is dropped before it finishes (a caller's timeout) counts as a failure.
//! [`Emitter::emit_required`] is `emit` for a caller that fails its operation on an error: that
//! fact is not lost and is not counted.
//!
//! The Go package of the same directory produces the same bytes from the same input, and the
//! shared vectors of `vectors/` keep the two in step.

mod canonical;
mod policy;
mod record;
mod table;
mod uuid;

use std::collections::HashMap;
use std::fmt;
use std::future::Future;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::{SystemTime, UNIX_EPOCH};

use atoom_common::refusal::new as refusal;
use atoom_transport::atoom::transport::v1alpha1::{
    AuditMessage, EmitRequest, TransportServiceClient,
};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use connectrpc::ConnectError;
use connectrpc::client::{ClientConfig, ClientTransport};
use connectrpc::error::ErrorCode;
use serde_json::{Map, Value};

pub use canonical::canonical;
pub use policy::{POLICY_VERSION, check_policy, max_cost, valid_release};
pub use table::{Activity, DEFS_NAME, Row, Table, check_documents};
pub use uuid::new_uuid_v7;

/// The result of every call: a Connect error with atoom's `Refusal` detail, whose reason and field
/// path name a rule and a member, never a value.
pub type Result<T> = std::result::Result<T, ConnectError>;

/// The OCSF release every record pins (record 0004: the first release pins 1.9.0).
pub const OCSF_VERSION: &str = "1.9.0";
/// The longest string a record may carry, in bytes (record 0004: free text is bounded).
pub const STRING_MAX: usize = 1024;
/// The largest record, in bytes.
pub const RECORD_MAX: usize = 16 << 10;

/// The purpose of the key a pseudonym is made under (record 0011, "The purposes audit declares").
pub const PSEUDONYM_PURPOSE: &str = "pseudonym";
const PSEUDONYM_BYTES: usize = 16;

/// OCSF's `status_id`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[repr(i64)]
pub enum Status {
    /// The action succeeded.
    #[default]
    Success = 1,
    /// The action failed.
    Failure = 2,
}

/// The action of an event, as a row of the product's action table names it (record 0004: the table is code).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Action {
    /// `metadata.event_code`, e.g. `key.sign`.
    pub name: String,
    /// The OCSF class, e.g. 6003 API Activity.
    pub class_uid: u32,
    /// The OCSF activity within the class, e.g. 99 Other.
    pub activity_id: u32,
    /// `activity_name`. Required when `activity_id` is 99 (record 0004: "`activity_name` only for
    /// activity 99 or where the table names one"), e.g. `Sign` or `Open` for key use.
    pub activity_name: String,
}

/// The person or external actor an event concerns.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Subject {
    /// The tenant whose pseudonym key is used.
    pub tenant: String,
    /// The kind of identity (record 0011, "Pseudonyms"), e.g. `person`, `workload` or `client`. It
    /// is part of the pseudonym's input, so the same identifier of two kinds gives two pseudonyms.
    /// Required.
    pub kind: String,
    /// The stable identifier of that kind: the subject of a person; the upstream's issuer and
    /// subject for an identity with no assigned subject; the workload id or client id of a workload
    /// or client; a product's own stable subject id for a person of a product. The emitter turns it
    /// into a pseudonym and never sends it.
    pub value: String,
}

/// The outcome of a policy decision: OCSF's `security_control` `action_id`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(i64)]
pub enum Decision {
    /// Allowed (`action_id` 1).
    Allowed = 1,
    /// Denied (`action_id` 2).
    Denied = 2,
}

impl Decision {
    pub(crate) fn caption(self) -> &'static str {
        match self {
            Decision::Allowed => "Allowed",
            Decision::Denied => "Denied",
        }
    }
}

/// The decision that gated an action (record 0004: part of the record of the action it gates).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct PolicyRef {
    /// The decision id: `policy.uid`. Required, because the OCSF policy object needs a uid; a
    /// digest or a list of policies without one is refused.
    pub decision_id: String,
    /// The digest of the policy bundle: `policy.version`.
    pub digest: String,
    /// The policies that took part: `authorizations[].policy.uid`.
    pub policy_ids: Vec<String>,
    /// `action_id` and `authorizations[].decision`; `None` records none.
    pub decision: Option<Decision>,
    /// The authorization results of an action that several decisions gated, such as a read of several tenants that
    /// asked policy once for each (REQ-AUDIT-074). When it is not empty these are `authorizations`, one entry for
    /// each, in this order, and `policy_ids` is not used; `policy` and `action_id` stay the one decision named above,
    /// which the caller chooses as the one that settled the action (the denying one when one denied). With none here
    /// the record is as it always was. At most [`RESULTS_MAX`].
    pub results: Vec<Decided>,
}

/// The most results one record names (the `authorizations` of the generated schemas hold 128).
pub const RESULTS_MAX: usize = 128;

/// One entry of `authorizations`: the verdict of a decision and the policy that determined it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Decided {
    /// `authorizations[].policy.uid`; empty for a decision no policy determined, which has the verdict alone.
    pub policy_id: String,
    /// `authorizations[].decision`.
    pub decision: Option<Decision>,
}

/// The thing an Entity Management action changed (OCSF `entity`): a key of the signing ring, a data key and the wrap of
/// it for a recipient, a document at an export destination. `kind` is a closed word that names the kind of thing and
/// is the row's own (the schema of the row holds it to that word). `name`, `uid` and `version` identify it; at least one
/// of `name` and `uid` is required. They are names, ids and counts the component knows, never text a request carried.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Entity {
    /// `entity.type`.
    pub kind: String,
    /// `entity.name`; empty for none.
    pub name: String,
    /// `entity.uid`; empty for none.
    pub uid: String,
    /// `entity.version`: a decimal number, such as the generation of a data key; empty for none.
    pub version: String,
}

/// One audit event.
#[derive(Clone, Debug, Default)]
pub struct Event {
    /// The action.
    pub action: Action,
    /// The outcome.
    pub status: Status,
    /// A closed reason, e.g. `grant`; empty on success.
    pub status_code: String,
    /// `metadata.tenant_uid`; empty is the operator's own tenant (the writer fills it).
    pub tenant: String,
    /// The person concerned, as `user.uid` (a pseudonym).
    pub subject: Option<Subject>,
    /// The decision that gated this action.
    pub policy: Option<PolicyRef>,
    /// The product object, `unmapped.<product>`: the product's own facts that OCSF has no attribute
    /// for (record 0004, "One product object"). Strings, booleans, integers within +-(2^53-1), lists
    /// of strings and nested objects. A float, a null or a list of anything else is refused, and so
    /// is the member `lost`, which only the emitter writes.
    pub product_object: Map<String, Value>,
    /// The correlation id for `metadata.correlation_uid`. When `None`, the emitter asks
    /// [`Config::correlation`] for the id of the call being served, so a handler need not pass it.
    /// An id that is not 1 to 64 characters of `[A-Za-z0-9_-]` is dropped.
    pub correlation_id: Option<String>,
    /// The thing an Entity Management action (class 3004) changed; required there and refused elsewhere.
    pub entity: Option<Entity>,
    /// The source of an API Activity action (class 6003; OCSF `src_endpoint.name`): the peer the transport named (from the
    /// platform identity of the caller), the node whose request a fact answers, or the component's own name for what it
    /// started itself. `unknown` says the transport could not name the peer. Never an address, and never text a request
    /// carried. Required for API Activity, refused elsewhere.
    pub caller: String,
    /// What an API Activity action did (class 6003; OCSF `api.operation`): the Connect method, the store verb or the route, a
    /// closed word the component chooses and never text a request carried. Required for API Activity, refused elsewhere.
    pub operation: String,
    /// Says the record is worth more than the name of its subject: when the [`Mac`] cannot make the pseudonym, the record
    /// is written as if it had no subject (`user.uid` `unknown` in an Authentication), and one warning says so. It is for a
    /// refusal, which is audited whatever else fails (record 0005); a fact that must name its subject leaves it `false` and
    /// fails when the pseudonym cannot be made.
    pub subject_best_effort: bool,
}

/// One `Mac` of the `pseudonym` purpose: what the emitter asks of the host of atoom's `seal` block
/// (record 0011, "Pseudonyms") to make the pseudonym of a subject.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct MacRequest {
    /// The purpose of the key: always [`PSEUDONYM_PURPOSE`].
    pub purpose: String,
    /// The tenant whose key is used.
    pub tenant: String,
    /// The domain label, [`Pseudonyms::label`], which the host binds into the MAC.
    pub label: String,
    /// The domain-separated input: the scope, the tenant, the kind of identity and its identifier,
    /// each with a 4-byte big-endian length in front.
    pub input: Vec<u8>,
    /// The epoch of the key: the UTC month of the record as `YYYYMM`.
    pub epoch: String,
}

/// A future of the result of a [`Mac`].
pub type MacFuture<'a> = Pin<Box<dyn Future<Output = Result<Vec<u8>>> + Send + 'a>>;

/// The keyed hash a pseudonym is made with: HMAC-SHA384 under the tenant's `pseudonym` key of the
/// epoch (record 0011), served by the host of atoom's `seal` block. The key never leaves the host,
/// and the emitter holds no key. A product implements this over its host's client.
pub trait Mac: Send + Sync {
    /// The MAC of the request, at least 16 bytes. An error stops the emit, unless the event allows
    /// its subject to go unnamed ([`Event::subject_best_effort`]).
    fn mac(&self, request: MacRequest) -> MacFuture<'_>;
}

/// How the emitter makes pseudonyms.
#[derive(Clone)]
pub struct Pseudonyms {
    /// The host's `Mac`.
    pub mac: Arc<dyn Mac>,
    /// The domain label of the pseudonym's input (record 0011). It is the product's (record 0018):
    /// the emitter has no default, and a changed label changes every pseudonym. sluis's is
    /// `sluis-pseudonym-v1`. Not empty.
    pub label: String,
}

impl fmt::Debug for Pseudonyms {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Pseudonyms")
            .field("label", &self.label)
            .finish_non_exhaustive()
    }
}

/// What an [`Emitter`] needs. The names are audit's neutral terms (record 0003); a product maps
/// its own words to them.
#[derive(Clone)]
pub struct Config {
    /// The product: `metadata.product.name`, and the name of the product object
    /// `unmapped.<product>` (record 0003).
    pub product: String,
    /// The release of the product: `metadata.product.version`.
    pub release: String,
    /// The scope the record belongs to: `metadata.log_name`, and the second half of
    /// `actor.user.uid` (`<component>@<scope>`).
    pub scope: String,
    /// The emitting component: `actor.user.uid` (`<component>@<scope>`), and the `application` or
    /// `service` of the classes that require one.
    pub component: String,
    /// The product's action table. The emitter refuses an action that is not a row and a record
    /// that does not validate against the schema of its row.
    pub table: Arc<Table>,
    /// The emit socket of atoom's transport port. It is dialled at the first call, with
    /// `atoom_socket::dial`, unless [`Emitter::with_transport`] gives another client transport.
    pub transport_socket: PathBuf,
    /// How pseudonyms are made; `None` for a component that never names a subject, which then
    /// cannot send a [`Subject`].
    pub pseudonyms: Option<Pseudonyms>,
    /// The correlation id of the call being served, read when an event gives none; `None` is
    /// `atoom_socket::call::correlation`. A product whose server keeps the call elsewhere gives its
    /// own reader.
    pub correlation: Option<fn() -> Option<String>>,
    /// The clock; `None` is the system clock. A test injects one.
    pub now: Option<fn() -> SystemTime>,
}

impl fmt::Debug for Config {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Config")
            .field("product", &self.product)
            .field("release", &self.release)
            .field("scope", &self.scope)
            .field("component", &self.component)
            .field("transport_socket", &self.transport_socket)
            .field("pseudonyms", &self.pseudonyms)
            .finish_non_exhaustive()
    }
}

/// A future of an emit to the transport.
type EmitFuture<'a> = Pin<Box<dyn Future<Output = Result<()>> + Send + 'a>>;

/// The transport client behind an emitter, whatever its client transport.
trait Sink: Send + Sync {
    fn emit(&self, req: EmitRequest) -> EmitFuture<'_>;
}

impl<T> Sink for TransportServiceClient<T>
where
    T: ClientTransport,
    <T::ResponseBody as connectrpc::http_body::Body>::Error: fmt::Display,
{
    fn emit(&self, req: EmitRequest) -> EmitFuture<'_> {
        Box::pin(async move {
            TransportServiceClient::emit(self, req).await?;
            Ok(())
        })
    }
}

/// Builds audit messages and hands them to the transport. It is cheap to clone and safe to share;
/// the clones share one process name and one set of totals of lost facts.
#[derive(Clone)]
pub struct Emitter {
    product: String,
    component: String,
    scope: String,
    release: String,
    transport: Arc<dyn Sink>,
    pseudonyms: Option<Pseudonyms>,
    correlation: fn() -> Option<String>,
    now: fn() -> SystemTime,
    lost: Arc<Lost>,
    table: Arc<Table>,
}

/// The tenants an emitter keeps a total for at most, so that a caller cannot grow the map with
/// tenants of its choosing. Past the bound a loss for another tenant is counted in the operator's
/// own bucket (key `""`), and one warning says so.
pub(crate) const MAX_LOST_TENANTS: usize = 256;

/// The running total for one tenant since the process started. `since` is the time of the first
/// loss that has not been reported yet; `reported` is the largest total a delivered record has
/// carried.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub(crate) struct TenantLoss {
    pub(crate) total: i64,
    pub(crate) since: i64,
    pub(crate) reported: i64,
}

#[derive(Default)]
pub(crate) struct LostState {
    pub(crate) by_tenant: HashMap<String, TenantLoss>,
    overflow: bool,
    /// The time (Unix ms) of the last warning that a record went without its subject, and how many records did since.
    subject_warned: Option<i64>,
    subject_unnamed: i64,
}

/// The least time between two warnings that a subject could not be named: while the host of the `Mac` is down every refusal of a
/// component that names its subject may be such a record, and a warning for each would be the flood the best-effort record is
/// there to avoid.
pub(crate) const SUBJECT_WARN_EVERY_MS: i64 = 60_000;

/// The name of one emitter for the life of its process (16 random hex digits), and its totals.
pub(crate) struct Lost {
    process: String,
    state: Mutex<LostState>,
}

/// What a record carries: the process, its running total for the record's tenant, and the time of
/// the first loss not yet reported. The zero total is none.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct LostNote {
    pub(crate) process: String,
    pub(crate) total: i64,
    pub(crate) since: i64,
}

impl LostState {
    /// The key a loss of the tenant is counted under: the tenant, or the operator's own bucket for a
    /// tenant too long to be one or when the bound is hit.
    pub(crate) fn bucket(&mut self, tenant: &str, process: &str) -> String {
        if self.by_tenant.contains_key(tenant) {
            return tenant.to_owned();
        }
        if tenant.len() > STRING_MAX || self.by_tenant.len() >= MAX_LOST_TENANTS {
            if !self.overflow && !tenant.is_empty() {
                self.overflow = true;
                tracing::warn!(
                    process,
                    bound = MAX_LOST_TENANTS,
                    "audit: too many tenants have lost facts; further losses are counted in the operator's own tenant"
                );
            }
            return String::new();
        }
        tenant.to_owned()
    }

    pub(crate) fn record_loss(&mut self, tenant: &str, at: i64, process: &str) {
        let k = self.bucket(tenant, process);
        let l = self.by_tenant.entry(k).or_default();
        if l.total == l.reported {
            l.since = at; // a new run of losses starts
        }
        l.total = l.total.saturating_add(1).min(canonical::MAX_SAFE_INT);
    }
}

impl Lost {
    fn new() -> Self {
        let mut b = [0u8; 8];
        uuid::os_random(&mut b);
        Self {
            process: b.iter().map(|x| format!("{x:02x}")).collect(),
            state: Mutex::default(),
        }
    }

    fn state(&self) -> std::sync::MutexGuard<'_, LostState> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Logs, at most once in [`SUBJECT_WARN_EVERY_MS`], that a record was written without its subject, with how many others
    /// were written without one since the last warning. Tenant-free on purpose: the log of the process is read by the
    /// operator, whoever the tenant is.
    fn warn_subject_unnamed(&self, now_ms: i64, action: &str) {
        let since_last = {
            let mut st = self.state();
            if st
                .subject_warned
                .is_some_and(|at| now_ms.saturating_sub(at) < SUBJECT_WARN_EVERY_MS)
            {
                st.subject_unnamed += 1;
                return;
            }
            st.subject_warned = Some(now_ms);
            std::mem::take(&mut st.subject_unnamed)
        };
        tracing::warn!(
            process = self.process.as_str(),
            action,
            since_last_warning = since_last,
            "audit: a subject could not be named; the record is written without it"
        );
    }

    /// The note a record of the tenant carries: its own total, while a record that carries it has
    /// not been delivered, and never another tenant's.
    fn note_for(&self, tenant: &str) -> LostNote {
        match self.state().by_tenant.get(tenant) {
            Some(l) if l.total > l.reported => LostNote {
                process: self.process.clone(),
                total: l.total,
                since: l.since,
            },
            _ => LostNote::default(),
        }
    }

    /// Records that a record carrying `n` reached the transport, and says so in the log.
    fn reported(&self, tenant: &str, n: &LostNote) {
        if n.total == 0 {
            return;
        }
        {
            let mut st = self.state();
            match st.by_tenant.get_mut(tenant) {
                Some(l) if n.total > l.reported => l.reported = n.total,
                _ => return,
            }
        }
        // Tenant-free on purpose: the log of the process is read by the operator, whoever the tenant is.
        tracing::warn!(
            process = n.process.as_str(),
            total = n.total,
            since = n.since,
            "audit: facts were lost and a record now reports them"
        );
    }
}

/// One emit in flight. A best-effort attempt that does not reach the transport adds one to the
/// total of its tenant when it is dropped, whether it failed or was cut off (a caller's timeout):
/// that is why this is a guard and not a `match` on the result.
struct Attempt<'a> {
    emitter: &'a Emitter,
    tenant: String,
    /// The fact is lost when this attempt fails (best effort).
    counted: bool,
    delivered: bool,
}

impl Drop for Attempt<'_> {
    fn drop(&mut self) {
        if self.delivered || !self.counted {
            return;
        }
        let at = unix_ms((self.emitter.now)());
        let lost = &self.emitter.lost;
        lost.state().record_loss(&self.tenant, at, &lost.process);
    }
}

fn valid_correlation(id: &str) -> bool {
    (1..=64).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

fn unix_ms(t: SystemTime) -> i64 {
    t.duration_since(UNIX_EPOCH)
        .ok()
        .and_then(|d| i64::try_from(d.as_millis()).ok())
        .unwrap_or(0)
}

/// `YYYYMM` of a Unix time in milliseconds, in UTC (days to civil date by Howard Hinnant's
/// algorithm).
fn epoch_of(ms: i64) -> String {
    let z = ms.div_euclid(86_400_000) + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    format!("{year:04}{month:02}")
}

impl Emitter {
    /// Returns an emitter. Nothing is dialled until the first call, so a transport that is not up
    /// yet does not fail construction.
    ///
    /// # Errors
    ///
    /// `invalid_argument` when the product, the release, the scope, the component or the label of
    /// the pseudonyms is empty or too long.
    pub fn new(cfg: Config) -> Result<Self> {
        let label = cfg.pseudonyms.as_ref().map_or("-", |p| p.label.as_str());
        for (v, path) in [
            (cfg.product.as_str(), "product"),
            (cfg.release.as_str(), "release"),
            (cfg.scope.as_str(), "scope"),
            (cfg.component.as_str(), "component"),
            (label, "pseudonyms.label"),
        ] {
            if v.is_empty() {
                return Err(refusal(ErrorCode::InvalidArgument, "schema", path));
            }
            if v.len() > STRING_MAX {
                return Err(refusal(ErrorCode::InvalidArgument, "bound", path));
            }
        }
        let t = atoom_socket::dial(&cfg.transport_socket);
        let transport: Arc<dyn Sink> =
            Arc::new(TransportServiceClient::new(t.transport(), t.config()));
        Ok(Self {
            product: cfg.product,
            component: cfg.component,
            scope: cfg.scope,
            release: cfg.release,
            transport,
            pseudonyms: cfg.pseudonyms,
            correlation: cfg.correlation.unwrap_or(atoom_socket::call::correlation),
            now: cfg.now.unwrap_or(SystemTime::now),
            lost: Arc::new(Lost::new()),
            table: cfg.table,
        })
    }

    /// Uses another client transport for atoom's transport port instead of `atoom_socket::dial` of
    /// [`Config::transport_socket`]: a product whose own client carries its call context, or a
    /// client in process. The emitter's totals of lost facts are kept.
    #[must_use]
    pub fn with_transport<T>(mut self, transport: T, config: ClientConfig) -> Self
    where
        T: ClientTransport,
        <T::ResponseBody as connectrpc::http_body::Body>::Error: fmt::Display,
    {
        self.transport = Arc::new(TransportServiceClient::new(transport, config));
        self
    }

    /// Uses another action table: a product's release has a catalogue of its own (record 0004). The
    /// emitter refuses an action that is not a row of the table and a record that does not validate
    /// against the schema of its row.
    #[must_use]
    pub fn with_table(mut self, table: Arc<Table>) -> Self {
        self.table = table;
        self
    }

    /// Hands the event to the transport and returns once the transport has accepted it
    /// (`EmitRequest.blocking` is false). An error is returned to the caller; the library neither
    /// buffers nor retries.
    ///
    /// This is for a best-effort fact: when it fails the fact is gone, so the emitter counts it for
    /// the event's tenant, and the next record of that tenant carries the total (record 0005, "Loss
    /// is counted while the process lives"). A call that is dropped before it finishes counts as a
    /// failure.
    /// A caller that fails its operation when the emit fails uses
    /// [`emit_required`](Self::emit_required).
    ///
    /// # Errors
    ///
    /// A refusal of the event, the [`Mac`]'s error, or the transport's.
    pub async fn emit(&self, ev: Event) -> Result<()> {
        self.send(ev, false, true).await
    }

    /// As [`emit`](Self::emit), for a caller that fails its operation when the emit fails: the
    /// fact is not lost, because the operation did not happen, so a failure is not counted.
    ///
    /// # Errors
    ///
    /// As [`emit`](Self::emit).
    pub async fn emit_required(&self, ev: Event) -> Result<()> {
        self.send(ev, false, false).await
    }

    /// Returns only when the transport says the message is durable (record 0002, "The transport";
    /// `blocking` is true). The caller decides what a failure means; the library does not retry.
    /// A failure is not counted as a lost fact: the caller fails the operation the fact belongs
    /// to. A caller that carries on when this fails uses
    /// [`emit_blocking_best_effort`](Self::emit_blocking_best_effort).
    ///
    /// # Errors
    ///
    /// As [`emit`](Self::emit).
    pub async fn emit_blocking(&self, ev: Event) -> Result<()> {
        self.send(ev, true, false).await
    }

    /// Waits for durability like [`emit_blocking`](Self::emit_blocking), for a caller that carries
    /// on when it fails (a tightening change, such as a revocation, which a full spool must never
    /// block: record 0005, "Wait, never fail"). The
    /// fact is then lost, so a failure is counted as for [`emit`](Self::emit).
    ///
    /// # Errors
    ///
    /// As [`emit`](Self::emit).
    pub async fn emit_blocking_best_effort(&self, ev: Event) -> Result<()> {
        self.send(ev, true, true).await
    }

    /// The total of best-effort facts this process has failed to deliver, over all tenants, since
    /// it started. It names no tenant; a component can export it as a metric.
    #[must_use]
    pub fn lost(&self) -> i64 {
        self.lost
            .state()
            .by_tenant
            .values()
            .fold(0_i64, |n, l| n.saturating_add(l.total))
            .min(canonical::MAX_SAFE_INT)
    }

    /// Builds the record with the note its tenant owes, and sends it. Nothing is taken or put
    /// back: a total only grows, and a record that carries it and reaches the transport, even if
    /// its caller then times out, is repeated harmlessly by the next record.
    async fn send(&self, ev: Event, blocking: bool, counted: bool) -> Result<()> {
        let tenant = ev.tenant.clone();
        let mut attempt = Attempt {
            emitter: self,
            tenant: tenant.clone(),
            counted,
            delivered: false,
        };
        let note = self.lost.note_for(&tenant);
        let (record, uid, carried) = self.build(ev, note).await?;
        let req = EmitRequest {
            message: AuditMessage {
                uid,
                record,
                ..AuditMessage::default()
            }
            .into(),
            blocking,
            ..EmitRequest::default()
        };
        self.transport.emit(req).await?;
        attempt.delivered = true;
        self.lost.reported(&tenant, &carried);
        Ok(())
    }

    /// Builds the message of an event without sending it: the record as RFC 8785 bytes, and its
    /// `metadata.uid`. Tests and the command line use it. It carries no note and changes no total.
    ///
    /// # Errors
    ///
    /// A refusal of the event, or the [`Mac`]'s error.
    pub async fn record(&self, ev: Event) -> Result<(Vec<u8>, String)> {
        let (rec, uid, _) = self.build(ev, LostNote::default()).await?;
        Ok((rec, uid))
    }

    /// Makes the record. When the note makes it larger than `RECORD_MAX`, the record is built
    /// without it and the note stays owed: a fact is never lost to the note, and the note is not
    /// lost to a large fact. Returns the note the record carries.
    async fn build(&self, ev: Event, note: LostNote) -> Result<(Vec<u8>, String, LostNote)> {
        record::validate_event(&ev, &self.product)?;
        // REQ-AUDIT-022: an action that is not in the table has no schema to hold its record to.
        self.table.check_action(&ev.action)?;
        let time_ms = unix_ms((self.now)());
        let pseudonym = match &ev.subject {
            Some(s) => match self.pseudonym(s, time_ms).await {
                Ok(p) => Some(p),
                Err(_) if ev.subject_best_effort => {
                    self.lost.warn_subject_unnamed(time_ms, &ev.action.name);
                    None
                }
                Err(e) => return Err(e),
            },
            None => None,
        };
        let uid = new_uuid_v7(time_ms);
        let ambient = (self.correlation)();
        let correlation = ev
            .correlation_id
            .as_deref()
            .or(ambient.as_deref())
            .filter(|c| valid_correlation(c));
        let input = |lost: LostNote| record::RecordInput {
            product: &self.product,
            component: &self.component,
            scope: &self.scope,
            release: &self.release,
            uid: &uid,
            time_ms,
            correlation,
            pseudonym: pseudonym.as_deref(),
            ev: &ev,
            lost,
        };
        let (rec, carried) = match record::build_record(&input(note.clone())) {
            Ok(rec) => (rec, note),
            Err(_) if note.total > 0 => (
                record::build_record(&input(LostNote::default()))?,
                LostNote::default(),
            ),
            Err(e) => return Err(e),
        };
        // REQ-AUDIT-099: the record, the note of lost facts included, validates against the schema of its action
        // before it leaves the process.
        self.table.validate(&rec)?;
        Ok((rec, uid.clone(), carried))
    }

    /// Asks the [`Mac`] for the keyed hash of the subject under the tenant's `pseudonym` key, with
    /// the epoch of the record's month, and formats it by record 0011, "Pseudonyms":
    /// `p1.<YYYYMM>.<first 128 bits, base64url, no padding>`.
    async fn pseudonym(&self, s: &Subject, time_ms: i64) -> Result<String> {
        let Some(p) = &self.pseudonyms else {
            // Never fall back to the raw subject.
            return Err(refusal(ErrorCode::FailedPrecondition, "keys", "subject"));
        };
        let epoch = epoch_of(time_ms);
        let mac = p
            .mac
            .mac(MacRequest {
                purpose: PSEUDONYM_PURPOSE.into(),
                tenant: s.tenant.clone(),
                label: p.label.clone(),
                input: pseudonym_input(&self.scope, s),
                epoch: epoch.clone(),
            })
            .await?;
        if mac.len() < PSEUDONYM_BYTES {
            return Err(refusal(ErrorCode::Internal, "schema", "subject"));
        }
        Ok(format_pseudonym(&epoch, &mac))
    }
}

/// The `Mac` input of a pseudonym (record 0011, "Pseudonyms"): the scope, the tenant, the kind of
/// identity and its identifier, each with a 4-byte big-endian length in front. The label is the
/// request's own, which the host binds in. The length prefixes make the parts unambiguous:
/// ("ab","c") and ("a","bc") differ.
pub(crate) fn pseudonym_input(scope: &str, s: &Subject) -> Vec<u8> {
    let mut b = Vec::new();
    for part in [scope, &s.tenant, &s.kind, &s.value] {
        // Every part is at most STRING_MAX bytes (checked before this is called).
        b.extend_from_slice(&u32::try_from(part.len()).unwrap_or(u32::MAX).to_be_bytes());
        b.extend_from_slice(part.as_bytes());
    }
    b
}

/// `p1.<epoch>.<first 128 bits of the MAC, base64url, no padding>`; `mac` holds at least 16 bytes.
fn format_pseudonym(epoch: &str, mac: &[u8]) -> String {
    format!(
        "p1.{epoch}.{}",
        URL_SAFE_NO_PAD.encode(&mac[..PSEUDONYM_BYTES])
    )
}

#[cfg(test)]
mod table_tests;
#[cfg(test)]
mod tests;
