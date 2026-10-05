# Diagnose a scheduled job that is failing or silent

## Purpose

Find out why a CronJob (verify, purge, clock-sync, the notary) is failing or has
stopped running, and get its error back.

## Preconditions

- `kubectl` access to the installation's namespace.
- For the clock-sync job: the reference clock's address (`ntp` in the job's
  configuration).

## Before you start

- **A CronJob that fails says nothing.** Its pods are deleted with the Job, so by
  the time anybody looks there is no log left. The tell is `lastSuccessfulTime`.
- **The jobs are configuration-shaped, so the causes are too:** a bucket or a
  prefix the process never received, a role missing one verb, a signing key it may
  not use. Each says so in one line and then exits, which is why the re-run is
  worth more than any amount of staring at the Job's events.
- **Delete the probe afterwards.** It is not in anybody's git, and a sync reports it.
- **The clock-sync job never sets the clock.** Whatever runs the machine does that.
  The offset it reports is the correction this clock needs: positive means it is
  behind.

## Steps

1. **Look for a job that never succeeded.**

   ```
   kubectl -n <ns> get cronjobs -o custom-columns=\
   NAME:.metadata.name,LAST:.status.lastScheduleTime,SUCCESS:.status.lastSuccessfulTime
   ```

   Expected: a `lastScheduleTime` with no (or an old) `lastSuccessfulTime` is a job
   failing on every run. Roll back: none.

2. **Get the error back** by running it again and keeping the pod.

   ```
   kubectl -n <ns> create job --from=cronjob/<name> <name>-probe
   kubectl -n <ns> logs job/<name>-probe
   ```

   Verify: the log names the bad key, bucket, role or key. Roll back:
   `kubectl -n <ns> delete job <name>-probe`.

3. **For clock-sync,** the same command by hand:

   ```
   audit clock-sync --ntp <server> --ntp <server> --sink <url> [--max-offset 1s]
   ```

   It compares this machine's clock with the references and records the reading as
   `audit.clock.synchronised`. A failure means either that the offset is larger
   than `--max-offset` or that no reference answered; the report says which. The
   reading is recorded either way when a reference did answer, because an hour
   whose timestamps are suspect is the hour an auditor most wants the measurement
   from. Nothing is recorded when no reference answered, because the clock was not
   checked and saying it was would be worse than a red job.

## Afterwards

Alert on the CronJob rather than on the archive: a verification that never ran
leaves nothing in the archive to notice.
