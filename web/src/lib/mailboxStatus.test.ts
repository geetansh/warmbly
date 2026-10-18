import { describe, expect, it } from "vitest";
import type AccountStatus from "@/lib/api/models/app/analytics/AccountStatus";
import { cloudStandingUnavailable, mailboxActivityLabel, mailboxColdActivity } from "./mailboxStatus";

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

describe("mailbox list label", () => {
    const idle: Activity = { in_campaign: false };
    const sending: Activity = { in_campaign: true };
    const resting: Activity = { in_campaign: true, send_lifecycle: { state: "resting" } };
    const reserve: Activity = { in_campaign: true, send_lifecycle: { state: "reserve" } };

    it("ranks Active > Sending > Reserve > Warming > Resting > Idle", () => {
        expect(mailboxActivityLabel(sending, true)).toBe("Active");
        expect(mailboxActivityLabel(sending, false)).toBe("Sending");
        expect(mailboxActivityLabel(reserve, true)).toBe("Reserve");
        expect(mailboxActivityLabel(reserve, false)).toBe("Reserve");
        expect(mailboxActivityLabel(resting, true)).toBe("Warming");
        expect(mailboxActivityLabel(idle, true)).toBe("Warming");
        expect(mailboxActivityLabel(resting, false)).toBe("Resting");
        expect(mailboxActivityLabel(idle, false)).toBe("Idle");
    });

    it("reads a missing status as idle unless warmup runs", () => {
        expect(mailboxActivityLabel(undefined, false)).toBe("Idle");
        expect(mailboxActivityLabel(undefined, true)).toBe("Warming");
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
