import { toJson } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Collapse from "@mui/material/Collapse";
import FormControlLabel from "@mui/material/FormControlLabel";
import IconButton from "@mui/material/IconButton";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import ListSubheader from "@mui/material/ListSubheader";
import MenuItem from "@mui/material/MenuItem";
import Select from "@mui/material/Select";
import Stack from "@mui/material/Stack";
import Switch from "@mui/material/Switch";
import Tab from "@mui/material/Tab";
import Table from "@mui/material/Table";
import TableBody from "@mui/material/TableBody";
import TableCell from "@mui/material/TableCell";
import TableHead from "@mui/material/TableHead";
import TableRow from "@mui/material/TableRow";
import Tabs from "@mui/material/Tabs";
import TextField from "@mui/material/TextField";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { Fragment, useMemo, useState } from "react";

import { Outcome_Result, RecordSchema, type Record as AuditRecord } from "../gen/audit/v1/record_pb.js";
import { compileQualifiers, qualifier } from "../qualifiers.js";
import { useAudit, useFacets, useRecord, useSearch, useTail } from "./hooks.js";

export interface AuditViewProps {
  /** The profiles this caller may read, in the order to show them. */
  profiles: string[];
  /** The profile to open on; default the first. */
  profile?: string;
  /** The qualifier box's starting text. */
  query?: string;
  /** Fields to count in the sidebar; empty hides it. Default action, outcome. */
  facets?: string[];
  pageSize?: number;
  /** A link to one record, for "copy link"; without it the button is hidden. */
  permalink?: (profile: string, id: string) => string;
}

const ranges: { label: string; since: string }[] = [
  { label: "Last hour", since: "1h" },
  { label: "Last 24 hours", since: "24h" },
  { label: "Last 7 days", since: "7d" },
  { label: "Last 30 days", since: "30d" },
  { label: "Any time", since: "" },
];

/**
 * An audit trail, read: profiles along the top, the qualifier box and a time
 * range, counts to narrow by, and the records as sentences, newest first.
 * A row opens to the record itself, with a button beside each value to narrow
 * to it or away from it, its link, and whether a verified digest covers it.
 *
 * It holds no credentials and signs nobody in: it asks through the client the
 * AuditProvider was given, and shows what the caller's grant lets through.
 */
export function AuditView({ profiles, profile: initial, query = "", facets = ["action", "outcome"], pageSize = 50, permalink }: AuditViewProps) {
  const [profile, setProfile] = useState(initial ?? profiles[0] ?? "");
  const [text, setText] = useState(query);
  const [applied, setApplied] = useState(query);
  const [since, setSince] = useState("24h");
  const [live, setLive] = useState(false);

  const full = [applied, since ? `since:${since}` : ""].filter(Boolean).join(" ");
  // Recompiled when the applied text or the range changes, not on every key.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const compiled = useMemo(() => compileQualifiers(full), [full]);
  const search = useSearch(profile, compiled.filter, pageSize);
  const tail = useTail(profile, compiled.filter, live);
  const counts = useFacets(profile, compiled.filter, facets);

  const narrow = (token: string) => {
    const next = [applied, token].filter(Boolean).join(" ");
    setText(next);
    setApplied(next);
  };

  if (profiles.length === 0) {
    return <Alert severity="info">No audit profile is readable with this sign-in.</Alert>;
  }

  const records = live ? [...tail.records, ...search.records] : search.records;

  return (
    <Stack spacing={2}>
      {profiles.length > 1 && (
        <Tabs value={profile} onChange={(_, p: string) => setProfile(p)} variant="scrollable">
          {profiles.map((p) => (
            <Tab key={p} value={p} label={p} />
          ))}
        </Tabs>
      )}

      <Stack direction={{ xs: "column", md: "row" }} spacing={1} sx={{ alignItems: { md: "flex-start" } }}>
        <TextField
          fullWidth
          size="small"
          label="Narrow"
          placeholder="actor:alice outcome:failure,denied target:credential"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") setApplied(text);
          }}
          error={compiled.errors.length > 0}
          helperText={compiled.errors[0] ?? "Press Enter to apply. field:value, a trailing * for a prefix, a leading - to exclude."}
          slotProps={{ htmlInput: { "aria-label": "Narrow" } }}
        />
        <Select size="small" value={since} onChange={(e) => setSince(e.target.value)} sx={{ minWidth: 160 }}
          inputProps={{ "aria-label": "Time range" }}>
          {ranges.map((r) => (
            <MenuItem key={r.label} value={r.since}>
              {r.label}
            </MenuItem>
          ))}
        </Select>
        <FormControlLabel control={<Switch checked={live} onChange={(e) => setLive(e.target.checked)} />} label="Live" />
      </Stack>

      {live && tail.error && (
        <Alert severity="warning">
          Live updates are not available here: {tail.error.message}. They need a searcher that orders by recorded time,
          which is the index; the archive scan cannot.
        </Alert>
      )}
      {search.error && <Alert severity="error">{explain(search.error.code, search.error.message)}</Alert>}

      <Stack direction={{ xs: "column", md: "row" }} spacing={2} sx={{ alignItems: "flex-start" }}>
        {facets.length > 0 && !counts.error && counts.facets.length > 0 && (
          <Box component="nav" aria-label="Counts" sx={{ minWidth: 220, maxWidth: { md: 280 } }}>
            {counts.facets.map((f) => (
              <List key={f.field} dense subheader={<ListSubheader disableSticky>{f.field}</ListSubheader>}>
                {f.values.slice(0, 10).map((v) => (
                  <ListItemButton key={v.value} onClick={() => narrow(qualifier(facetQualifier(f.field), v.value))}>
                    <ListItemText primary={v.value || "(none)"} secondary={String(v.count)} />
                  </ListItemButton>
                ))}
              </List>
            ))}
          </Box>
        )}

        <Box sx={{ flex: 1, minWidth: 0, width: "100%" }}>
          <Table size="small" aria-label="Records">
            <TableHead>
              <TableRow>
                <TableCell sx={{ width: 180 }}>When</TableCell>
                <TableCell>What happened</TableCell>
                <TableCell sx={{ width: 120 }}>Outcome</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {records.map((r) => (
                <RecordRow key={r.id} record={r} profile={profile} narrow={narrow} permalink={permalink} />
              ))}
              {!search.loading && records.length === 0 && !search.error && (
                <TableRow>
                  <TableCell colSpan={3}>
                    <Typography color="text.secondary">Nothing recorded matches.</Typography>
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
          {search.more && (
            <Button onClick={search.loadMore} disabled={search.loading} sx={{ mt: 1 }}>
              Older
            </Button>
          )}
        </Box>
      </Stack>
    </Stack>
  );
}

// facetQualifier is the qualifier name for a facet's field.
function facetQualifier(field: string): string {
  switch (field) {
    case "actor_kind":
      return "actor.kind";
    case "target_type":
      return "target";
    case "tenant_id":
      return "tenant";
    default:
      return field;
  }
}

function explain(code: string, message: string): string {
  switch (code) {
    case "permission_denied":
    case "7":
      return `This sign-in may not read that: ${message}`;
    case "unauthenticated":
    case "16":
      return "The sign-in has ended. Sign in again to read the trail.";
    default:
      return message;
  }
}

interface RecordRowProps {
  record: AuditRecord;
  profile: string;
  narrow: (token: string) => void;
  permalink?: ((profile: string, id: string) => string) | undefined;
}

function RecordRow({ record: r, profile, narrow, permalink }: RecordRowProps) {
  const { sentencer } = useAudit();
  const [open, setOpen] = useState(false);
  const when = r.occurredAt ? timestampDate(r.occurredAt) : undefined;
  return (
    <Fragment>
      <TableRow hover onClick={() => setOpen(!open)} sx={{ cursor: "pointer", "& > td": { borderBottom: open ? "none" : undefined } }}>
        <TableCell>
          {when && (
            <Tooltip title={when.toISOString()}>
              <span>{when.toLocaleString()}</span>
            </Tooltip>
          )}
        </TableCell>
        <TableCell>
          <Tooltip title={sentencer.summary(r) ?? r.action}>
            <span>{sentencer.sentence(r)}</span>
          </Tooltip>
        </TableCell>
        <TableCell>
          <OutcomeChip result={r.outcome?.result ?? Outcome_Result.UNSPECIFIED} />
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell colSpan={3} sx={{ py: 0 }}>
          <Collapse in={open} unmountOnExit>
            <Detail record={r} profile={profile} narrow={narrow} permalink={permalink} />
          </Collapse>
        </TableCell>
      </TableRow>
    </Fragment>
  );
}

function OutcomeChip({ result }: { result: Outcome_Result }) {
  const word = (Outcome_Result[result] ?? "unknown").toLowerCase();
  const color = result === Outcome_Result.SUCCESS ? "success" : result === Outcome_Result.UNSPECIFIED ? "default" : "warning";
  return <Chip size="small" label={word} color={color} variant="outlined" />;
}

// The values a row offers to narrow by, and the qualifier each fills.
function pivots(r: AuditRecord): { label: string; field: string; value: string }[] {
  const out: { label: string; field: string; value: string }[] = [
    { label: "action", field: "action", value: r.action },
    { label: "tenant", field: "tenant", value: r.tenantId },
  ];
  if (r.actor?.id) out.push({ label: "actor", field: "actor", value: r.actor.id });
  if (r.subject?.id) out.push({ label: "subject", field: "subject", value: r.subject.id });
  if (r.outcome) out.push({ label: "outcome", field: "outcome", value: (Outcome_Result[r.outcome.result] ?? "").toLowerCase() });
  r.targets.forEach((t) => out.push({ label: `target ${t.type}`, field: "target", value: `${t.type}:${t.id}` }));
  if (r.context?.requestId) out.push({ label: "request", field: "request", value: r.context.requestId });
  if (r.context?.traceId) out.push({ label: "trace", field: "trace", value: r.context.traceId });
  return out.filter((p) => p.value !== "");
}

function Detail({ record: r, profile, narrow, permalink }: RecordRowProps) {
  const at = useRecord(profile, r.id);
  const provenance = at.response?.provenance;
  const json = useMemo(() => JSON.stringify(toJson(RecordSchema, r, { useProtoFieldName: true }), null, 2), [r]);
  return (
    <Stack spacing={1} sx={{ py: 1 }}>
      <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap", alignItems: "center" }}>
        {provenance?.verifiedAt ? (
          <Chip size="small" color="success" label={`Verified ${timestampDate(provenance.verifiedAt).toLocaleString()}`}
            title={`Covered by digest ${provenance.digestId}`} />
        ) : provenance ? (
          <Chip size="small" label="Not yet covered by a verified digest" title={provenance.objectKey} />
        ) : null}
        {permalink && (
          <Button size="small" onClick={() => void navigator.clipboard?.writeText(permalink(profile, r.id))}>
            Copy link
          </Button>
        )}
      </Stack>
      <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap" }}>
        {pivots(r).map((p) => (
          <Chip
            key={p.label + p.value}
            size="small"
            label={`${p.label}: ${p.value}`}
            onClick={() => narrow(qualifier(p.field, p.value))}
            onDelete={() => narrow(qualifier(p.field, p.value, true))}
            deleteIcon={<IconButton size="small" aria-label={`Exclude ${p.label} ${p.value}`} component="span">−</IconButton>}
          />
        ))}
      </Stack>
      <Box component="pre" sx={{ m: 0, p: 1, bgcolor: "action.hover", overflowX: "auto", fontSize: 12 }}>
        {json}
      </Box>
    </Stack>
  );
}
