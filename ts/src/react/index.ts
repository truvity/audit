// @truvity/audit/react: hooks over the query client, and a default MUI view.
//
// The host passes a client over its own transport — its own credentials — to
// <AuditProvider>; nothing here signs anybody in.

export {
  AuditProvider,
  useAccess,
  useAudit,
  useFacets,
  useRecord,
  useSearch,
  useTail,
  type Access,
  type AuditProviderProps,
  type Facets,
  type ReadError,
  type RecordAt,
  type Search,
  type Tail,
} from "./hooks.js";
export { AuditView, type AuditViewProps } from "./AuditView.js";
