import { describe, expect, it } from "vitest";
import type AccountStatus from "@/lib/api/models/app/analytics/AccountStatus";
import type { CloudLinkMailboxRow, PoolLinkMailboxState } from "@/lib/api/models/app/cloudlink/CloudLink";
import type Inbox from "@/lib/api/models/app/emails/Inbox";
import {
    cloudStandingGap,
    cloudStandingGapCopy,
    cloudStandingUnavailable,
    mailboxActivityLabel,
    mailboxColdActivity,
    mailboxReading,
    tallyMailboxes,
} from "./mailboxStatus";

type Activity = Pick<AccountStatus, "send_lifecycle" | "in_campaign">;

describe("cold rotation activity", () => {
    it.each(["resting", "reserve"] as const)("shows %s without a campaign on this instance", (state) => {
        expect(mailboxColdActivity({ in_campaign: false, send_lifecycle: { state } })).toEqual({ resting: true, sending: false });
    });

    it.each(["resting", "reserve"] as const)("never reports %s as campaign sending", (state) => {
        expect(mailboxColdActivity({ in_campaign: true, send_lifecycle: { state } })).toEqual({ resting: true, sending: false });
    });

    it("keeps older status responses without lifecycle compatible", () => {
        expect(mailboxColdActivity({ in_campaign: true })).toEqual({ resting: false, sending: true });
        expect(mailboxColdActivity({ in_campaign: false })).toEqual({ resting: false, sending: false });
        expect(mailboxColdActivity()).toEqual({ resting: false, sending: false });
    });

    it("does not call a mailbox sending just because it is available for cold rotation", () => {
        expect(mailboxColdActivity({ in_campaign: false, send_lifecycle: { state: "active" } })).toEqual({ resting: false, sending: false });
    });
});

describe("Cloud standing availability", () => {
    it("tells a missing Cloud report from a real block", () => {
        expect(cloudStandingUnavailable({ reason: "cloud_evidence_unavailable" })).toBe(true);
        expect(cloudStandingUnavailable({ reason: "complaints" })).toBe(false);
        expect(cloudStandingUnavailable(null)).toBe(false);
        expect(cloudStandingUnavailable(undefined)).toBe(false);
    });
});

describe("one mailbox's Warmbly Cloud standing", () => {
    const conflict = { code: "pool_link_mailbox_conflict", message: "This address already exists in the linked Cloud workspace. Contact Warmbly support." };
    const reported = (state: PoolLinkMailboxState["health"]) => ({ health: state, warmup: { enabled: true, paused: false } }) as PoolLinkMailboxState;
    const row = (id: string, over: Partial<CloudLinkMailboxRow> = {}): CloudLinkMailboxRow => ({
        id, email: `${id}@test.example`, name: "", provider: "smtp_imap", status: "active",
        enrolled: true, managed: false, enrollment_state: "active",
        cloud: reported({ state: "healthy", score: 100, spam_score: 0 }), ...over,
    });
    const box = (id: string, over: Partial<Inbox> = {}) => ({ id, email: `${id}@test.example`, status: "active", auth_state: "passing", ...over }) as Inbox;
    const healthy = (id: string): AccountStatus => ({ id, health: { status: "healthy", score: 100, issues: [] }, errors: [], in_campaign: false }) as unknown as AccountStatus;
    // What the backend reports while Cloud has no current status: a warning with exactly that one issue.
    const unknown = (id: string): AccountStatus => ({
        ...healthy(id),
        health: { status: "warning", score: 100, issues: ["Warmbly Cloud status unavailable"] },
        warmup_health: { state: "blocked", reason: "cloud_evidence_unavailable" },
    }) as AccountStatus;

    it("names why it is not known, keyed off the enrollment state", () => {
        expect(cloudStandingGap(row("a", { enrollment_state: "pending_enroll", cloud: null, enrollment_error: conflict }))).toEqual({ kind: "enrolling", error: conflict, update: false });
        expect(cloudStandingGap(row("a", { enrollment_state: "pending_enroll", cloud: null }))).toEqual({ kind: "enrolling", error: null, update: false });
        // A resend for a mailbox Cloud already holds, such as a new sign-in.
        expect(cloudStandingGap(row("a", { enrollment_state: "pending_enroll", enrollment_error: conflict }))).toEqual({ kind: "enrolling", error: conflict, update: true });
        expect(cloudStandingGap(row("a", { enrollment_state: "pending_remove", cloud: null }))).toEqual({ kind: "removing" });
        expect(cloudStandingGap(row("a", { cloud: null }))).toEqual({ kind: "unreported" });
        expect(cloudStandingGap(row("a", { cloud: reported({ state: "blocked", score: 0, spam_score: 0, reason: "cloud_evidence_unavailable" }) }))).toEqual({ kind: "unreported" });
        expect(cloudStandingGap(row("a"))).toBeNull();
        expect(cloudStandingGap(row("a", { enrolled: false, cloud: null }))).toBeNull();
        expect(cloudStandingGap(undefined, unknown("a"))).toEqual({ kind: "unreported" });
        expect(cloudStandingGap(undefined, healthy("a"))).toBeNull();
    });

    it("surfaces Cloud's refusal, which is the actionable part", () => {
        const error = { code: "pool_link_mailbox_conflict", message: "The address exists" };
        const copy = cloudStandingGapCopy({ kind: "enrolling", error, update: false });
        expect(copy).toMatchObject({ refused: true, label: "Cloud refused" });
        expect(copy.title).toContain("has not accepted this mailbox: The address exists.");
        expect(cloudStandingGapCopy({ kind: "enrolling", error: null, update: false })).toMatchObject({ refused: false, label: "Enrolling" });
    });

    it("words a refused resend as an update Cloud did not take, not a mailbox it never accepted", () => {
        const copy = cloudStandingGapCopy({ kind: "enrolling", error: { code: "pool_link_invalid_credentials", message: "Sign-in failed" }, update: true });
        expect(copy).toMatchObject({ refused: true, label: "Update refused" });
        expect(copy.title).not.toContain("has not accepted this mailbox");
        expect(copy.title).toContain("keeps the copy it already had");
        expect(cloudStandingGapCopy({ kind: "enrolling", error: null, update: true }).label).toBe("Updating");
    });

    it("counts a lone unknown-standing warning as pending Cloud, a kept refusal and anything more as needing attention", () => {
        const enrolling = row("a", { enrollment_state: "pending_enroll", cloud: null });
        expect(mailboxReading(box("a"), unknown("a"), enrolling)).toBe("pending_cloud");
        expect(mailboxReading(box("a"), healthy("a"), enrolling)).toBe("pending_cloud");
        expect(mailboxReading(box("a"), unknown("a"), row("a", { cloud: null }))).toBe("pending_cloud");
        expect(mailboxReading(box("a"), unknown("a"), row("a", { enrollment_state: "pending_remove", cloud: null }))).toBe("pending_cloud");
        // A conflict never clears on its own, so it is not merely waiting.
        const stuck = row("a", { enrollment_state: "pending_enroll", cloud: null, enrollment_error: conflict });
        expect(mailboxReading(box("a"), unknown("a"), stuck)).toBe("cloud_refused");
        const another = { ...unknown("a"), health: { status: "warning", score: 80, issues: ["Warmbly Cloud status unavailable", "Low inbox placement"] } } as AccountStatus;
        expect(mailboxReading(box("a"), another, stuck)).toBe("issue");
        const failing = { ...unknown("a"), errors: [{ id: "e", error_code: "AUTH", severity: "CRITICAL", title: "Sign-in failed", message: "", created_at: new Date() }] } as AccountStatus;
        expect(mailboxReading(box("a"), failing, stuck)).toBe("issue");
        expect(mailboxReading(box("a", { status: "inactive" }), unknown("a"), stuck)).toBe("issue");
        expect(mailboxReading(box("a"), healthy("a"), row("a"))).toBe("warming");
    });

    it("keeps one stuck enrollment from hiding every other mailbox", () => {
        const ids = ["a", "b", "c", "d", "e", "f", "g", "h", "i"];
        const boxes = ids.map((id) => box(id, { warmup: null }));
        const rows = new Map<string, CloudLinkMailboxRow>([
            ["a", row("a", { enrollment_state: "pending_enroll", cloud: null, enrollment_error: conflict })],
            ["b", row("b", { enrollment_state: "pending_enroll", cloud: null, enrollment_error: conflict })],
            ["c", row("c", { enrollment_state: "pending_enroll", cloud: null })],
            ["d", row("d")],
            ["e", row("e", { cloud: { ...reported({ state: "healthy", score: 100, spam_score: 0 }), warmup: { enabled: true, paused: true } } as PoolLinkMailboxState })],
        ]);
        const statuses = new Map(ids.map((id) => [id, ["a", "b", "c"].includes(id) ? unknown(id) : healthy(id)]));
        statuses.set("f", { ...healthy("f"), health: { status: "error", score: 40, issues: ["Account is not active"] } } as AccountStatus);
        // Two kept conflicts need attention beside f's real error; c only waits on Cloud.
        expect(tallyMailboxes(boxes, (id) => statuses.get(id), (id) => rows.get(id))).toEqual({
            total: 9, healthy: 4, warming: 1, issues: 3, pendingCloud: 1,
        });
    });

    it("leaves a mailbox with no status yet in the total only", () => {
        const tally = tallyMailboxes([box("a"), box("b")], (id) => (id === "a" ? healthy(id) : undefined), () => undefined);
        expect(tally).toEqual({ total: 2, healthy: 1, warming: 0, issues: 0, pendingCloud: 0 });
    });
});
