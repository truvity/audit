import type { Record as AuditRecord } from "./gen/audit/v1/record_pb.js";
import { common } from "./catalogue/common.js";
import { recordArguments, renderMessage } from "./messages.js";

/**
 * What the viewer needs from one catalogue: each action's summary and its
 * message template per locale. `audit messages <catalogue.yaml>` prints it as
 * JSON; an application ships its own catalogue's alongside its console.
 */
export interface Sentences {
  source: string;
  version: string;
  locales?: string[];
  actions: { [action: string]: { summary?: string; message?: { [locale: string]: string } } };
}

/**
 * Turns records into sentences, from the catalogues it was given and the
 * common catalogue, which describes the audit component's own actions and is
 * always included.
 *
 * A record names the catalogue version it was written under. That version's
 * template is used when present; otherwise the newest version of the source
 * that has the action. A record is never left without words: a template that
 * does not render falls back to the action's summary, and a record whose
 * action no catalogue knows reads as its action name.
 */
export class Sentencer {
  private readonly bySource = new Map<string, Sentences[]>();

  constructor(
    catalogues: Sentences[] = [],
    readonly locale = "en",
  ) {
    for (const c of [common, ...catalogues]) {
      const list = this.bySource.get(c.source) ?? [];
      list.push(c);
      // Newest first, by version, so a fallback prefers the latest words.
      list.sort((a, b) => compareVersions(b.version, a.version));
      this.bySource.set(c.source, list);
    }
  }

  /** The sentence for a record. */
  sentence(r: AuditRecord): string {
    const action = this.action(r);
    if (!action) return r.action;
    const template = action.message?.[this.locale] ?? action.message?.en ?? firstValue(action.message);
    if (template) {
      const text = renderMessage(template, recordArguments(r), this.locale);
      if (text !== undefined && text.trim() !== "") return text;
    }
    return action.summary || r.action;
  }

  /** The summary of what a record's action is, for a tooltip or a heading. */
  summary(r: AuditRecord): string | undefined {
    return this.action(r)?.summary;
  }

  private action(r: AuditRecord) {
    const versions = this.bySource.get(r.source) ?? [];
    const exact = versions.find((c) => c.version === r.catalogueVersion)?.actions[r.action];
    if (exact) return exact;
    for (const c of versions) {
      const found = c.actions[r.action];
      if (found) return found;
    }
    return undefined;
  }
}

function firstValue(m: { [k: string]: string } | undefined): string | undefined {
  return m ? Object.values(m)[0] : undefined;
}

// compareVersions orders dotted numeric versions; anything else compares as text.
function compareVersions(a: string, b: string): number {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  if (pa.some(Number.isNaN) || pb.some(Number.isNaN)) return a.localeCompare(b);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
}
