// Which sending domains are landing warmup mail in spam, above the mailbox
// list. A chip narrows the list to that domain's mailboxes.
import { useMemo } from "react";
import { XIcon } from "lucide-react";
import ScrollStrip from "@/components/ui/scroll-strip";
import type { PlacementRate } from "@/lib/api/models/app/analytics/WarmupPlacement";
import { cn } from "@/lib/utils";
import { fmtNum, spamByDomain } from "./placement";

export default function SpamByDomainStrip({
    rows,
    query,
    onQuery,
}: {
    rows: { email: string; rate?: PlacementRate | null }[];
    query: string;
    onQuery: (q: string) => void;
}) {
    const domains = useMemo(() => spamByDomain(rows), [rows]);
    if (domains.length === 0) return null;
    return (
        <div className="flex items-center gap-2 min-w-0">
            <span className="shrink-0 text-[10px] uppercase tracking-[0.14em] text-slate-400 font-medium">Spam by domain</span>
            <ScrollStrip className="flex-1 min-w-0" innerClassName="flex items-center gap-1.5" activeKey={query}>
                {domains.map((d) => {
                    const q = `@${d.domain}`;
                    const active = query.trim().toLowerCase() === q;
                    return (
                        <button
                            key={d.domain}
                            type="button"
                            data-active={active ? "true" : undefined}
                            onClick={() => onQuery(active ? "" : q)}
                            title={`${fmtNum(d.spam)} of ${fmtNum(d.delivered)} warmup emails from ${d.domain} landed in spam at Google, Microsoft and Yahoo over the last 7 days (${d.mailboxes} ${d.mailboxes === 1 ? "mailbox" : "mailboxes"} with spam). ${active ? "Click to show every mailbox." : "Click to show only this domain's mailboxes."}`}
                            className={cn(
                                "shrink-0 h-6 px-2 rounded-md border inline-flex items-center gap-1.5 text-[11.5px] transition-colors",
                                active ? "border-sky-300 bg-sky-50 text-sky-700" : "border-slate-200 text-slate-700 hover:border-slate-300",
                            )}
                        >
                            <span className="size-1.5 rounded-full bg-rose-500" />
                            <span className="truncate max-w-[16ch]">{d.domain}</span>
                            <span className="font-mono tabular-nums text-rose-600">
                                {fmtNum(d.spam)}
                                <span className="opacity-60">/{fmtNum(d.delivered)}</span>
                            </span>
                            {active && <XIcon className="w-3 h-3 text-sky-500" aria-hidden />}
                        </button>
                    );
                })}
        </ScrollStrip>
        </div>
    );
}
