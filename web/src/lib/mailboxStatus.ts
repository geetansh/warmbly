import type Inbox from "@/lib/api/models/app/emails/Inbox";
import type AccountStatus from "@/lib/api/models/app/analytics/AccountStatus";
import type { CloudLinkEnrollmentError, CloudLinkMailboxRow } from "@/lib/api/models/app/cloudlink/CloudLink";
import { cloudWarmupPaused } from "./cloudWarmup";
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

export type CloudStandingGap =
    // update: Cloud already holds the mailbox and keeps warming it while a resend (a new sign-in) is pending.
    | { kind: "enrolling"; error: CloudLinkEnrollmentError | null; update: boolean }
    | { kind: "removing" }
    | { kind: "unreported" };

// Why this one mailbox's Warmbly Cloud standing is not known yet; null when it is, or Cloud does not warm it.
export function cloudStandingGap(row?: CloudLinkMailboxRow | null, status?: Pick<AccountStatus, "warmup_health"> | null): CloudStandingGap | null {
    if (row?.enrolled) {
        if (row.enrollment_state === "pending_enroll") return { kind: "enrolling", error: row.enrollment_error ?? null, update: !!row.cloud };
        if (row.enrollment_state === "pending_remove") return { kind: "removing" };
        if (!row.cloud?.health || cloudStandingUnavailable(row.cloud.health)) return { kind: "unreported" };
    }
    return cloudStandingUnavailable(status?.warmup_health) ? { kind: "unreported" } : null;
}

export interface CloudStandingGapCopy {
    label: string;
    title: string;
    refused: boolean;
}

const sentence = (s: string) => (/[.!?]$/.test(s.trim()) ? s.trim() : `${s.trim()}.`);

export function cloudStandingGapCopy(gap: CloudStandingGap): CloudStandingGapCopy {
    switch (gap.kind) {
        case "enrolling":
            if (gap.update) {
                return gap.error
                    ? { label: "Update refused", title: `Warmbly Cloud refused this mailbox's update: ${sentence(gap.error.message)} It keeps the copy it already had; campaign sends wait until the update is accepted.`, refused: true }
                    : { label: "Updating", title: "Sending this mailbox's update to Warmbly Cloud. Campaign sends wait until it is accepted.", refused: false };
            }
            return gap.error
                ? { label: "Cloud refused", title: `Warmbly Cloud has not accepted this mailbox: ${sentence(gap.error.message)} Campaign sends wait until it does.`, refused: true }
                : { label: "Enrolling", title: "Waiting for Warmbly Cloud to accept this mailbox. Campaign sends wait until it reports a status.", refused: false };
        case "removing":
            return { label: "Leaving Cloud", title: "Being removed from Warmbly Cloud. Campaign sends wait until the removal finishes.", refused: false };
        default:
            return { label: "Awaiting Cloud", title: "Warmbly Cloud has not reported a current status for this mailbox. Campaign sends wait for it; nothing else is held.", refused: false };
    }
}

// cloud_refused needs attention, since a refusal does not clear by itself; pending_cloud is waiting on Cloud alone.
export type MailboxReading = "healthy" | "warming" | "issue" | "pending_cloud" | "cloud_refused";

// One mailbox's place in the summary. The unknown-standing warning adds exactly one issue, so a lone one is the gap itself.
export function mailboxReading(box: Inbox, status: AccountStatus, cloud?: CloudLinkMailboxRow | null): MailboxReading {
    if (box.status !== "active" || box.auth_state === "failing" || status.errors?.length) return "issue";
    const health = status.health?.status;
    const gapOnly = health === "healthy" || (health === "warning" && cloudStandingUnavailable(status.warmup_health) && (status.health.issues?.length ?? 0) <= 1);
    const gap = gapOnly ? cloudStandingGap(cloud, status) : null;
    if (gap) return gap.kind === "enrolling" && gap.error ? "cloud_refused" : "pending_cloud";
    if (health !== "healthy") return "issue";
    const warming = cloud?.enrolled ? !!cloud.cloud?.warmup?.enabled && !cloudWarmupPaused(cloud.cloud) : diagnosticWarmupActive(box);
    return warming ? "warming" : "healthy";
}

export interface MailboxTally {
    total: number;
    healthy: number;
    warming: number;
    issues: number;
    pendingCloud: number;
}

// Counts what is known; a mailbox with no status yet is in the total only.
export function tallyMailboxes(
    boxes: readonly Inbox[],
    statusFor: (id: string) => AccountStatus | undefined,
    cloudFor: (id: string) => CloudLinkMailboxRow | undefined,
): MailboxTally {
    const t: MailboxTally = { total: 0, healthy: 0, warming: 0, issues: 0, pendingCloud: 0 };
    for (const box of boxes) {
        t.total++;
        const status = statusFor(box.id);
        if (!status) continue;
        const reading = mailboxReading(box, status, cloudFor(box.id));
        if (reading === "pending_cloud") t.pendingCloud++;
        else if (reading === "issue" || reading === "cloud_refused") t.issues++;
        else t[reading]++;
    }
    return t;
}

// Account activity is not proof of deliverability or reputation readiness.
export default function mailboxDisplayStatus(box: Inbox): MailboxDisplayStatus {
    if (box.status !== "active") return "inactive";
    if (box.warmup_effective?.source === "cloud") {
        const state = box.warmup_effective.state;
        return state === "active" ? "warming" : state === "paused" ? "paused" : "healthy";
    }
    if (box.test_mode !== "off" && box.warmup && (box.warmup_paused_at || !diagnosticSendingAllowed(box))) return "paused";
    if (diagnosticWarmupActive(box)) return "warming";
    return "healthy";
}
