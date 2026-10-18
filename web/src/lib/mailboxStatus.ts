import type Inbox from "@/lib/api/models/app/emails/Inbox";
import type AccountStatus from "@/lib/api/models/app/analytics/AccountStatus";
import { diagnosticSendingAllowed, diagnosticWarmupActive } from "./diagnosticParticipation";

export type MailboxDisplayStatus = "healthy" | "warming" | "paused" | "inactive";

export function mailboxColdActivity(status?: Pick<AccountStatus, "send_lifecycle" | "in_campaign">) {
    const lifecycle = status?.send_lifecycle;
    const resting = !!lifecycle && lifecycle.state !== "active";
    return { resting, sending: !!status?.in_campaign && !resting };
}

export type MailboxActivityLabel = "Active" | "Sending" | "Reserve" | "Warming" | "Resting" | "Idle";

// One word for the list. Reserve is the owner's hold, so it outranks warming; resting does not.
export function mailboxActivityLabel(status: Pick<AccountStatus, "send_lifecycle" | "in_campaign"> | undefined, warming: boolean): MailboxActivityLabel {
    const { resting, sending } = mailboxColdActivity(status);
    if (sending) return warming ? "Active" : "Sending";
    if (status?.send_lifecycle?.state === "reserve") return "Reserve";
    if (warming) return "Warming";
    return resting ? "Resting" : "Idle";
}

// The standing Warmbly Cloud stands in for while it has no current report: sends wait, but it is no verdict.
export function cloudStandingUnavailable(h?: { reason?: string } | null) {
    return h?.reason === "cloud_evidence_unavailable";
}

// Account activity is not proof of deliverability or reputation readiness.
export default function mailboxDisplayStatus(box: Inbox): MailboxDisplayStatus {
    if (box.status !== "active") return "inactive";
    if (box.test_mode !== "off" && box.warmup && (box.warmup_paused_at || !diagnosticSendingAllowed(box))) return "paused";
    if (diagnosticWarmupActive(box)) return "warming";
    return "healthy";
}
