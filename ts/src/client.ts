import { createClient, type Client, type Transport } from "@connectrpc/connect";

import { QueryService } from "./gen/audit/v1/query_pb.js";

/** The query service's client. */
export type QueryClient = Client<typeof QueryService>;

/**
 * A client for the query service over the host's transport. The transport is
 * the host's because the credentials are: the viewer authenticates nothing
 * itself, it sends what the host's transport sends.
 *
 * For a browser, `createConnectTransport` from @connectrpc/connect-web, with
 * the service's base URL and whatever the host signs requests with. Asking for
 * proto field names keeps the JSON the same as the API reference shows:
 *
 *   createConnectTransport({ baseUrl, jsonOptions: { useProtoFieldName: true } })
 */
export function createQueryClient(transport: Transport): QueryClient {
  return createClient(QueryService, transport);
}
