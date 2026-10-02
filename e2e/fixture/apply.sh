#!/usr/bin/env bash
#
# Stands in for the platform on the local kind box: the database and its two
# roles, the wide stream, the archive bucket, and the digest chain's signing
# key pair — created under the EXACT names
# charts/audit/testdata/values/e2e.yaml gives the chart, read through
# e2e/fixture/names.go rather than repeated here by hand.
#
# Idempotent and re-runnable on a laptop: a password already in a Secret is
# kept, a role or database that already exists is left alone, a stream
# already there is not recreated.
set -euo pipefail
cd "$(dirname "$0")/../.."

KCTX=${KCTX:-kind-policy}
kubectl() { command kubectl --context "$KCTX" "$@"; }

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

eval "$(go run ./e2e/fixture/cmd/resolve -namespace "${NS:-audit-e2e}" -release "${RELEASE:-audit-e2e}")"

kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"

step "the writer role's credential"
if ! kubectl -n "$NAMESPACE" get secret "$WRITER_SECRET" >/dev/null 2>&1; then
  password=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')
  # The chart reads the password (the config names the variable it arrives in,
  # and carries the URL without it); the suite that connects from outside reads
  # the whole connection string.
  kubectl -n "$NAMESPACE" create secret generic "$WRITER_SECRET" \
    --from-literal=password="$password" \
    --from-literal=url="postgres://$WRITER_ROLE:$password@$DATABASE_HOST:5432/$DATABASE?sslmode=require"
fi

step "the query role's credential"
if ! kubectl -n "$NAMESPACE" get secret "$QUERY_SECRET" >/dev/null 2>&1; then
  password=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')
  kubectl -n "$NAMESPACE" create secret generic "$QUERY_SECRET" \
    --from-literal=password="$password" \
    --from-literal=url="postgres://$QUERY_ROLE:$password@$DATABASE_HOST:5432/$DATABASE?sslmode=require"
fi

# The two roles' passwords, read back from the Secrets above rather than
# the local variables above them: on a re-run those Secrets already existed
# and the passwords just generated were thrown away, so this is the only
# copy the roles below must actually agree with.
writer_password=$(kubectl -n "$NAMESPACE" get secret "$WRITER_SECRET" -o jsonpath='{.data.url}' | base64 -d | sed -n 's#.*://[^:]*:\([^@]*\)@.*#\1#p')
query_password=$(kubectl -n "$NAMESPACE" get secret "$QUERY_SECRET" -o jsonpath='{.data.url}' | base64 -d | sed -n 's#.*://[^:]*:\([^@]*\)@.*#\1#p')

step "the database and its two roles"
# Idempotent by construction rather than by catching an error: Postgres has
# no `CREATE ROLE IF NOT EXISTS`, so existence is asked first. The password
# is (re)applied every run, converging a role an earlier run created onto
# whatever the Secret actually hands the chart this run.
kubectl -n postgres exec -i deploy/postgres -c postgres -- psql -U postgres -v ON_ERROR_STOP=1 <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$WRITER_ROLE') THEN
    CREATE ROLE $WRITER_ROLE LOGIN;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$QUERY_ROLE') THEN
    CREATE ROLE $QUERY_ROLE LOGIN;
  END IF;
END
\$\$;
ALTER ROLE $WRITER_ROLE WITH PASSWORD '$writer_password';
ALTER ROLE $QUERY_ROLE WITH PASSWORD '$query_password';
SELECT 'CREATE DATABASE $DATABASE OWNER $WRITER_ROLE'
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '$DATABASE') \gexec
SQL

# A throwaway NATS CLI, the same image truvity/policy's own box fixture
# execs a Job with — it is a client, not a server this box runs, so it is
# not one of the box's own pinned versions.
nats_box_image="natsio/nats-box:0.18.0@sha256:abdc9f9f0120bb8adfbf674eb037d1551db55356eb198b7bd4ffed377f6950a6"

step "the stream"
if ! kubectl -n nats run "audit-fixture-stream-$RANDOM" --rm -i --restart=Never --image "$nats_box_image" --command -- sh -c "
    nats --server '$STREAM_URL' stream info '$STREAM' >/dev/null 2>&1 && exit 0
    nats --server '$STREAM_URL' stream add '$STREAM' \
      --subjects '$STREAM_SUBJECT' \
      --storage file --retention limits --discard old \
      --max-msgs=-1 --max-bytes=-1 --max-age=-1 --max-msg-size=-1 --max-consumers=-1 \
      --dupe-window=2m --replicas 1 --no-allow-rollup --no-deny-delete --no-deny-purge --defaults
  " >/tmp/audit-fixture-stream.log 2>&1; then
  cat /tmp/audit-fixture-stream.log >&2
  exit 1
fi
rm -f /tmp/audit-fixture-stream.log

step "the archive bucket"
kubectl -n object-store exec deploy/s3 -- sh -c "awslocal s3 mb s3://$BUCKET >/dev/null 2>&1 || true"

step "static S3 credentials for the box's stand-in"
kubectl -n "$NAMESPACE" get secret "$S3_CREDS_SECRET" >/dev/null 2>&1 || \
  kubectl -n "$NAMESPACE" create secret generic "$S3_CREDS_SECRET" \
    --from-literal=AWS_ACCESS_KEY_ID=test \
    --from-literal=AWS_SECRET_ACCESS_KEY=test

step "the digest chain's signing key"
# ed25519, PKCS8 — the one algorithm keys/signer.go's file-based Signer
# signs with (an ECDSA private key is a KMS or transit key's shape, never
# this one's: keys/signer.go refuses it with "is not a key this build signs
# with"). keys/signer.go's Verify accepts either, because a real deployment
# may sign with a managed ECDSA key instead — this fixture just never asks
# the file-based signer to be one.
if ! kubectl -n "$NAMESPACE" get secret "$DIGEST_KEY_SECRET" >/dev/null 2>&1; then
  keydir=$(mktemp -d)
  trap 'rm -rf "$keydir"' EXIT
  openssl genpkey -algorithm ed25519 -out "$keydir/key.pem" >/dev/null 2>&1
  openssl pkey -in "$keydir/key.pem" -pubout -out "$keydir/public.pem" >/dev/null 2>&1
  kubectl -n "$NAMESPACE" create secret generic "$DIGEST_KEY_SECRET" --from-file=key.pem="$keydir/key.pem"
  kubectl -n "$NAMESPACE" create secret generic "$VERIFY_PUBLIC_SECRET" --from-file=public.pem="$keydir/public.pem"
fi

echo
echo "the fixture is in place: $DATABASE_HOST/$DATABASE, roles $WRITER_ROLE and $QUERY_ROLE, stream $STREAM, bucket $BUCKET"
