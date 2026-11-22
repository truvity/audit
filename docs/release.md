# Releasing

One version for the whole repository ([record 0016](decisions/0016-distribution-and-the-lambda-shape.md)): a release tags every Go library at `X.Y.Z` and carries every Rust crate at the same version. audit releases libraries only today, the Go module `github.com/truvity/audit/emitter` and the Rust crate `audit-emitter`, and the versions start at 0.17.0, above the tags of the archived module. The flow is atoom's ([atoom's release checklist](https://github.com/truvity/atoom/blob/master/docs/release.md)), with the names of this repository and without its packages and images. This page is the checklist. The tags are made by hand; everything after the root tag is a workflow.

## Before the first tag (the owner, once)

- A tag ruleset on `v*` and `*/v*`: only the owner creates a tag, nobody updates or deletes one.
- Immutable releases on. The workflow cannot read this setting (a `GITHUB_TOKEN` has no Administration permission), so it checks the result: after it publishes, it reads `immutable` of the release and fails the job if that is not `true`.
- `check` (the aggregate job of `.github/workflows/check.yaml`) as a required status check on `master`.
- **Actions policy.** If the organisation allows only listed actions, `actions/attest-build-provenance` (pinned by SHA in `release.yaml`) must be allowed. Artifact attestations need a public repository, which this is.

## A release

1. Choose `X.Y.Z`, at least 0.17.0 (while the version is 0.x, a breaking change in any library raises the minor version of all).
2. On a branch: `just release-prepare X.Y.Z`. It sets the Rust workspace version and Cargo.lock, sets the `require` of every sibling Go module to `vX.Y.Z` (with `go mod edit`, never `go mod tidy`) and drops their `replace` lines, makes `go.work` resolve the siblings at that version to their directories, and writes the changelog section as the first heading of `CHANGELOG.md`. Edit the notes of the section if they need more than the list of libraries. Commit it as `release: X.Y.Z` and open the PR; CI runs every check and the candidate rehearsal.
3. **The candidate.** After the rebase merge, the push to `master` that changes the first heading of `CHANGELOG.md` to an untagged version starts `candidate.yaml`. It checks that the tree is prepared for the version, makes the source archive of the commit and its sums, builds and tests the archive (every Go module and the Rust workspace) on an amd64 and an arm64 runner, writes the evidence of what it smoked by digest, and shows that its gate refuses a changed byte, an extra or a missing file, another commit, a rehearsal's evidence and an incomplete smoke. It holds `contents: read` and nothing else. **Wait for it to be green on the merge commit**: the tag run refuses a commit with no green candidate. Its artifacts are kept for 30 days; an expired or failed candidate is built again by a new commit that changes the first `## ` heading line of `CHANGELOG.md` (append a date) or, for a failed job, by `gh run rerun --failed`; then tag that commit.
4. On the merge commit: `just release-tag X.Y.Z --dry-run` runs every check and lists the tags (`vX.Y.Z` and `emitter/vX.Y.Z`); `just release-tag X.Y.Z` then makes them, signed (`git tag -s`), all or none.
5. Push the library tags first (any number), then the root tag alone: GitHub starts no workflow for a push of more than three tags, and the release workflow starts on the root tag `vX.Y.Z`. The script prints both commands.
6. The release workflow (`release.yaml`):
   - `gate`: the tag is on `master`, the run of `check.yaml` on `master` is green, the tag set is whole, and a green candidate run holds the files of this commit;
   - `consume`: `go get` of every module through the public proxy and a Rust crate against the tag;
   - `attest`: holds every file to the evidence, and makes a GitHub provenance attestation of `SHA256SUMS` (the one job with an id-token);
   - `release`: holds every file again, creates the GitHub release as a **draft** with every asset, checks that the draft holds exactly those assets, makes it public, and reads the `immutable` field of the published release: the job fails if it is not `true` (immutable releases off): check the release by hand.
7. Check the result: `gh release view vX.Y.Z -R truvity/audit` and `gh attestation verify audit-X.Y.Z.tar.gz --repo truvity/audit`.

A tag is never moved. A wrong set is repaired with the next patch version.

## What the release holds

The notes (the changelog section), `audit-X.Y.Z.tar.gz` (the source of the tagged commit, every path under `audit-X.Y.Z/`) and `SHA256SUMS`. A consumer takes the libraries through the tags: `go get github.com/truvity/audit/emitter@vX.Y.Z`, and `audit-emitter = { git = "https://github.com/truvity/audit", tag = "vX.Y.Z" }` in Cargo. The crate is not on crates.io (`publish = false`): it depends on atoom's crates by git tag.

## The rehearsals

`just release-rehearsal` runs steps 2 to 5 on a throw-away copy of the working tree with fixture libraries. It makes no tag here and pushes nothing. `just release-candidate-test` runs the scope of a push, the check of the release files, the evidence gate and its mutants, and the publication (against a stand-in for `gh`) on fixtures. CI runs both on every pull request.

`candidate.yaml` is itself rehearsed (version `0.0.0`, artifacts kept for a day) on every pull request and every push to `master` that changes a manifest, the toolchain pins, `CHANGELOG.md`, `tools/release` or the workflows; the rest of the time that job does nothing and succeeds. On one machine: `just release-assemble` and then `just release-smoke`.
