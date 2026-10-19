import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";
import type { PlacementRate } from "@/lib/api/models/app/analytics/WarmupPlacement";
import SpamByDomainStrip from "./SpamByDomainStrip";

const rate = (inbox: number, spam: number): PlacementRate => ({
    window_days: 7, min_sample: 20, delivered: inbox + spam, inbox, tabs: 0, spam, inbox_rate: null, band: "collecting",
});

describe("spam by domain strip", () => {
    beforeAll(() => {
        HTMLElement.prototype.scrollBy = vi.fn();
    });

    it("hides when no domain has spam", () => {
        const { container } = render(<SpamByDomainStrip rows={[{ email: "a@fine.dev", rate: rate(5, 0) }]} query="" onQuery={() => {}} />);
        expect(container).toBeEmptyDOMElement();
    });

    it("filters the list to a domain and clears it on a second click", () => {
        const onQuery = vi.fn();
        const rows = [{ email: "adam@calloptixpro.com", rate: rate(3, 2) }];
        const { rerender } = render(<SpamByDomainStrip rows={rows} query="" onQuery={onQuery} />);
        fireEvent.click(screen.getByRole("button", { name: /calloptixpro\.com/ }));
        expect(onQuery).toHaveBeenLastCalledWith("@calloptixpro.com");
        rerender(<SpamByDomainStrip rows={rows} query="@calloptixpro.com" onQuery={onQuery} />);
        fireEvent.click(screen.getByRole("button", { name: /calloptixpro\.com/ }));
        expect(onQuery).toHaveBeenLastCalledWith("");
    });
});
