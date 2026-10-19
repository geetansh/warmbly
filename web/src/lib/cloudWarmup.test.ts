import { describe, expect, it } from "vitest";
import type { PoolLinkMailboxState } from "@/lib/api/models/app/cloudlink/CloudLink";
import { cloudWarmupPaused } from "./cloudWarmup";

// The same table as TestPoolLinkMailboxStateCloudWarmupState in internal/models/effective_warmup_test.go.
describe("cloudWarmupPaused matches the server's Cloud warmup rule", () => {
    const warming = { warmup: { enabled: true, paused: false } } as PoolLinkMailboxState;
    const cases: [string, PoolLinkMailboxState, boolean][] = [
        ["not reported", {} as PoolLinkMailboxState, true],
        ["warming", warming, false],
        ["paused", { ...warming, warmup: { ...warming.warmup!, paused: true } }, true],
        ["participation off", { ...warming, participation: { mode: "off", send: false, receive: false } }, true],
        ["diagnostic without send", { ...warming, participation: { mode: "diagnostic", send: false, receive: true } }, true],
        ["diagnostic with send", { ...warming, participation: { mode: "diagnostic", send: true, receive: false } }, false],
    ];
    it.each(cases)("%s", (_, state, paused) => {
        expect(cloudWarmupPaused(state)).toBe(paused);
    });
});
