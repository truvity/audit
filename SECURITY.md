# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately via
[GitHub Security Advisories](https://github.com/truvity/audit/security/advisories/new).

Do NOT open a public issue for security vulnerabilities.

## Supported Versions

Only the latest release is supported with security updates.

## Design properties that matter for reports

- Records are append-only. The store is expected to carry S3 Object Lock in
  compliance mode; see `docs/operations/s3-guide.md`.
- Every read of the audit trail is itself recorded.
- Subjects are referenced by identifiers or keyed pseudonyms, never by
  identity attributes. See `docs/decisions/0005-identity-tiers-and-pseudonymisation.md`.
- The split writer is the most privileged component: it holds unwrapped
  pseudonymisation keys in memory. Reports about it are especially welcome.
