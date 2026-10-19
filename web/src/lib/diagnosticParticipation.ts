import type Inbox from "@/lib/api/models/app/emails/Inbox";
import type { WarmupState } from "@/lib/api/models/app/emails/Inbox";

type Participation = Pick<Inbox, "test_mode" | "test_send_enabled" | "test_receive_enabled">;
type WarmupFields = Participation & Pick<Inbox, "warmup" | "warmup_paused_at" | "warmup_effective">;

export function diagnosticSendingAllowed(row: Participation): boolean {
    return row.test_mode == null || row.test_mode === "legacy" || row.test_mode === "diagnostic" && row.test_send_enabled === true;
}

// The server's answer when it gave one (it knows about Warmbly Cloud); the local columns only for an older server.
export function effectiveWarmupState(row: WarmupFields): WarmupState {
    if (row.warmup_effective) return row.warmup_effective.state;
    if (!row.warmup) return "off";
    return diagnosticSendingAllowed(row) && !row.warmup_paused_at ? "active" : "paused";
}

export function diagnosticWarmupActive(row: WarmupFields): boolean {
    return effectiveWarmupState(row) === "active";
}
