// @truvity/audit: read an audit trail from TypeScript.
//
// The client and the typed contract, the qualifier box compiled to the typed
// filter, and records rendered as the sentences their catalogues declare.
// React hooks and a default MUI view are under "@truvity/audit/react".

export { createQueryClient, type QueryClient } from "./client.js";
export { compileQualifiers, qualifier, qualifierNames, type Compiled } from "./qualifiers.js";
export { messageArguments, recordArguments, renderMessage, type Arguments, type ArgumentValue } from "./messages.js";
export { Sentencer, type Sentences } from "./sentences.js";
export { common as commonSentences } from "./catalogue/common.js";

export * from "./gen/audit/v1/record_pb.js";
export * from "./gen/audit/v1/query_pb.js";
