import { describe, expect, it } from "vitest";
import type { PlacementDay, PlacementRate } from "@/lib/api/models/app/analytics/WarmupPlacement";
import { observedCount, spamByDomain, spamSortValue, totals, viewDays } from "./placement";

const legacy: PlacementDay = {
    date: "2026-10-01", inbox: 1, tabs: 0, spam: 1, rescued: 0,
    sent: 10, delivered: 2, unconfirmed: 4, inbox_rate: 50, spam_rate: 50,
    rolling_inbox_rate: null, groups: [],
};

describe("placement observation scope", () => {
    it("preserves missing evidence from older APIs", () => {
        const day = viewDays([legacy], "all", 20, 7)[0];
        expect(day.unknown).toBeUndefined();
        expect(day.archived).toBeUndefined();
        expect(day.custom).toBeUndefined();
        expect(totals([day]).inboxRate).toBe(50);
    });

    it("keeps unclassified receipts visible but outside rate denominators", () => {
        const day = viewDays([{ ...legacy, unknown: 2, archived: 1, custom: 1 }], "all", 20, 7)[0];
        expect(observedCount(day)).toBe(6);
        expect(day.delivered).toBe(2);
        expect(totals([day]).inboxRate).toBe(50);
    });

    it("does not invent inbox placement for an unknown-only provider", () => {
        const day = viewDays([{ ...legacy, groups: [{ group: "google", inbox: 0, tabs: 0, spam: 0, rescued: 0, unknown: 2, archived: 1, custom: 1 }] }], "google", 1, 7)[0];
        expect(observedCount(day)).toBe(4);
        expect(day.rolling).toBeNull();
        expect(totals([day]).inboxRate).toBeNull();
        expect(day.sent).toBe(0);
    });
});

const rate = (inbox: number, spam: number): PlacementRate => ({
    window_days: 7, min_sample: 20, delivered: inbox + spam, inbox, tabs: 0, spam, inbox_rate: null, band: "collecting",
});

describe("spam at a glance", () => {
    it("sorts by spam share, then count, with no receipts last", () => {
        expect(spamSortValue(rate(3, 2))).toBeGreaterThan(spamSortValue(rate(9, 1)));
        expect(spamSortValue(rate(2, 2))).toBeGreaterThan(spamSortValue(rate(1, 1)));
        expect(spamSortValue(rate(0, 0))).toBe(-1);
        expect(spamSortValue(undefined)).toBe(-1);
    });

    it("rolls spam up by sending domain, worst first, leaving clean domains out", () => {
        const out = spamByDomain([
            { email: "adam@CallOptixPro.com", rate: rate(3, 2) },
            { email: "eve@calloptixpro.com", rate: rate(5, 0) },
            { email: "bob@other.io", rate: rate(1, 1) },
            { email: "clean@fine.dev", rate: rate(10, 0) },
            { email: "new@fresh.dev" },
        ]);
        expect(out).toEqual([
            { domain: "other.io", spam: 1, delivered: 2, mailboxes: 1 },
            { domain: "calloptixpro.com", spam: 2, delivered: 10, mailboxes: 1 },
        ]);
    });
});
