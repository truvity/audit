# Regulation and standards requirements

Context assumed: an EU-based provider of digital-identity, verifiable-
credential and trust-service-adjacent software, on a public cloud in the
EU. Not legal advice.

## GDPR

Logs with user ids, addresses or session ids are personal data. Lawful
basis: Art. 6(1)(c) where a law obliges logging, otherwise 6(1)(f) with
Recital 49 naming network and information security. No fixed retention;
CNIL suggests six months extendable to twelve. Art. 30 records describe
processing and are not the technical log; the audit trail is itself a
processing activity to register. Immutable structures: the EDPB's
guidelines 02/2025 on blockchain (v2.0, July 2026) say keyed hashes remain
personal data while the key exists, and that after deletion of the key the
hash should not be linkable, which is the basis for pseudonymisation with
key destruction. Art. 17(3)(b) and (e) exempt records under a legal duty.
Sources: https://gdpr-info.eu/recitals/no-49/ ,
https://gdpr-info.eu/art-17-gdpr/ ,
https://www.edpb.europa.eu/system/files/2026-07/edpb_guidelines_202502_blockchain_v2_en.pdf ,
https://www.edpb.europa.eu/system/files/2025-01/edpb_guidelines_202501_pseudonymisation_en.pdf

## eIDAS 2.0 and the wallet

Regulation (EU) 2024/1183 Art. 5a(4)(d): the wallet gives the user a log of
all transactions via a dashboard. Art. 5a(14): the wallet provider collects
nothing not necessary and does not combine wallet data with other data.
Art. 5a(16): unlinkability. CIR (EU) 2024/2979 Art. 9: wallet instances
log transactions with relying parties, whether completed or not, with
date, relying-party identity, types of data requested and presented, and
reason for non-completion; accessible to the provider only where necessary
and with explicit consent; exportable. ARF DASH_03b: the log shall not
contain the value of any attribute presented. ARF WIAM_12a: the wallet
provider cannot access the wallet unit log. Art. 24(2)(h): trust service
providers record and keep accessible all relevant information for evidence,
including after cessation.
Sources: https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=OJ:L_202401183 ,
https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=OJ:L_202402979 ,
https://eudi.dev/2.4.0/discussion-topics/h-transaction-logs-kept-by-the-wallet/ ,
https://eudi.dev/1.9.0/annexes/annex-2/annex-2-high-level-requirements/

## ETSI for trust service providers

EN 319 401 V3.2.1 clause 7.10: record and keep accessible all relevant
information for legal evidence; confidentiality and integrity; precise time
of key-management and clock events; time synchronised with UTC at least
once a day; retention as notified in the terms; logged so records cannot
be easily deleted or destroyed (write-only media, multi-site copies).
Clause 7.9.1: logs regularly reviewed, backed up for a predefined period,
protected from change, redundant logging, independent availability
monitoring. EN 319 411-1 clause 6.4.6: key-lifecycle logs kept at least
seven years after any certificate based on them ceases to be valid; EN 319
411-2: beyond termination. TS 119 461: identity-proofing evidence stored
tamper-proof; a qualified time-stamp proves time and protects against
tampering; deleted at the end of retention.
Sources: https://www.etsi.org/deliver/etsi_en/319400_319499/319401/03.02.01_60/en_319401v030201p.pdf ,
https://www.etsi.org/deliver/etsi_en/319400_319499/31941101/01.05.01_60/en_31941101v010501p.pdf ,
https://www.etsi.org/deliver/etsi_en/319400_319499/31941102/02.06.01_60/en_31941102v020601p.pdf ,
https://www.etsi.org/deliver/etsi_ts/119400_119499/119461/02.01.01_60/ts_119461v020101p.pdf

## NIS2

Trust service providers are in scope regardless of size; qualified ones are
essential entities. CIR (EU) 2024/2690 Annex 3.2.3 lists what to log:
traffic, user lifecycle, access to systems and applications, authentication,
all privileged access, changes to critical configuration and backups,
security-tool logs, resource use, physical access. 3.2.4 regular review;
3.2.5 maintain and back up logs for a predefined period and protect them;
3.2.6 synchronised time, redundant logging, independent monitoring. Art.
23: early warning within 24 hours, notification within 72, final report
within a month. ENISA's 2025 technical guidance: delete when retention
ends; log all access and changes to log files; authenticated NTP. The
Dutch transposition has applied since 15 August 2026.
Sources: https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=OJ:L_202402690 ,
https://www.nis-2-directive.com/NIS_2_Directive_Article_23.html ,
https://www.enisa.europa.eu/sites/default/files/2025-06/ENISA_Technical_implementation_guidance_on_cybersecurity_risk_management_measures_version_1.0.pdf ,
https://www.rijksoverheid.nl/actueel/nieuws/2026/07/07/cyberbeveiligingswet-en-wet-weerbaarheid-kritieke-entiteiten-vanaf-15-augustus-2026-van-kracht

## DORA

CDR (EU) 2024/1774 Art. 12: logging procedures identify events, retention
and protection; detail sufficient to detect anomalies; log access control,
identity management, capacity, change, operations, network; protect
against tampering, deletion and unauthorised access at rest, in transit
and in use; detect failures of logging systems; clocks synchronised to a
documented reliable reference. Retention set by the entity. Flows down to
suppliers by contract (Art. 30 of the regulation).
Source: https://eur-lex.europa.eu/eli/reg_del/2024/1774/oj/eng

## ISO/IEC 27001:2022 and SOC 2

A.8.15 logging: events produced, stored, protected and analysed; each
event with user id, activity, date and time, device or system identifier,
network addresses; users including administrators cannot amend or delete
their own logs. A.8.16 monitoring; A.8.17 clock synchronisation. SOC 2
CC7.2 and CC7.3: monitoring for anomalies and evidence of review across the
audit period.
Sources: https://www.iso.org/standard/27001 ,
https://www.isms.online/iso-27002/control-8-15-logging/

## PCI DSS v4.0.1 Requirement 10

10.2.1: user access to cardholder data, administrative actions, access to
audit logs, invalid access attempts, credential and privilege changes,
starting and stopping of logging, system-object changes. 10.2.2 per
record: user id, event type, date and time, success or failure, origination,
affected data or resource. 10.3 protection and file-integrity monitoring.
10.4 daily review of security events. 10.5.1 twelve months, three
immediately available. 10.6 time synchronisation.
Source: https://www.pcisecuritystandards.org/document_library/

## NIST and OWASP

SP 800-53 AU-3: what, when, where, source, outcome, identity; AU-3(3)
limit PII in audit records; AU-9 protect audit information, alert on
tampering, write-once media, separate system, cryptographic integrity,
dual authorisation for deletion; AU-10 non-repudiation; AU-11 retention;
AU-12 generation. OWASP Logging Cheat Sheet: when, where, who, what; never
log secrets, tokens, session ids, passwords, card data, sensitive personal
data; sanitise for log injection; record all access to logs. ASVS 5.0 V16:
log inventory, synchronised UTC time, protected logs, logically separate
system.
Sources: https://csf.tools/reference/nist-sp-800-53/r5/au/ ,
https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html ,
https://github.com/OWASP/ASVS/blob/v5.0.0/5.0/en/0x25-V16-Security-Logging-and-Error-Handling.md

## Tamper evidence and Object Lock

Cohasset's 2025 assessment: S3 Object Lock in compliance or governance mode
meets SEC 17a-4(f), FINRA 4511(c) and CFTC 1.31 non-rewriteable and non-
erasable requirements when properly configured; governance mode requires
procedural controls over bypass permissions. Compliance mode cannot be
shortened by any user including root. No EU regulator certifies a storage
product; a conformity assessment body evaluates the implementation against
the ETSI clause, for which compliance mode plus the assessment is accepted
evidence.
Sources: https://d1.awsstatic.com/onedam/marketing-channels/website/aws/en_US/whitepapers/compliance/Amazon-S3-Compliance-Assessment-2025.pdf ,
https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html

## Dutch healthcare (NEN 7513)

Per event: who (user id and role), what, when, which object or patient,
source system, on behalf of; changes to authorisations; enabling and
disabling of logging; all access to the log. Minimum five years for
medical-record access logs by decree. The Dutch DPA fined a hospital for,
among other things, not regularly reviewing its access logs.
Sources: https://zoek.officielebekendmakingen.nl/stcrt-2019-38007.pdf ,
https://autoriteitpersoonsgegevens.nl/uploads/imported/besluit_haga_-_ter_openbaarmaking.pdf

## Consolidated minimum

Fields: event time and ingest time in UTC; event type from a catalogue;
actor kind and identifier with delegation where it exists; source
including client address and service; action and outcome with reason;
target type and identifier; correlation id; tenant; integrity (digest
chain). Retention tiers: hot at least three months; security twelve
months by default; legal evidence seven years after expiry, published in
terms; billing seven years; deletion at end of period. Integrity: append-
only, logically separate system, compliance-mode Object Lock, digest
chain, verification job with alerting, synchronised time recorded daily.
Access: need-to-know reads, no self-deletion, dual authorisation for
deletion and hold release, every read logged. PII: identifiers as keyed
pseudonyms with deletable keys, negative list enforced at the SDK, an
inventory and a register entry.
