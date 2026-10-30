import { http } from "@/services/api/request";
import type { ModelChannel } from "@/stores/use-config-store";

export const NEW_API_BASE_URL = "https://video.sistinelove.cn/v1";
// The backend performs a dashboard login, account/key lookup or creation, and
// a model-catalog request. The shared JSON client defaults to four seconds.
const NEW_API_SYNC_TIMEOUT_MS = 120_000;

export type NewAPIConnectionSyncResponse = {
    channel: ModelChannel;
};

/** Credentials are submitted only for this sync request and are never kept in browser storage. */
export function syncNewAPIConnection(username: string, password: string) {
    return http.post<NewAPIConnectionSyncResponse>("/newapi/connection/sync", { username, password }, { timeout: NEW_API_SYNC_TIMEOUT_MS });
}
