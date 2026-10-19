#!/bin/sh
# Whether a mailbox is warming is models.ResolveEffectiveWarmup (Go) or
# warmupEnabledSQL / cloudWarmsSQL (SQL), because Warmbly Cloud warms enrolled
# mailboxes while email_accounts.warmup stays NULL. Reading the local column
# alone reports those mailboxes as "warmup off".
#
# The local column is still right for code that runs this instance's own
# warmup (schedulers, pool membership, worker weights, the write paths), so
# each file's count of raw reads is pinned in scripts/warmup-reads.baseline.
# A new raw read fails here. If it really is about local execution, raise that
# file's count in the baseline in the same change; otherwise use the effective
# helpers. A count that drops must be lowered too, so the baseline stays exact.
set -eu

main() {
    root=$(cd "$(dirname "$0")/.." && pwd)
    baseline="$root/scripts/warmup-reads.baseline"
    pattern='\.Warmup (!=|==) nil|\bwarmup IS (NOT )?NULL|IsWarmingActive\(\)|IsWarmupPaused\(\)'
    current=$(cd "$root" && grep -rEc --include='*.go' "$pattern" internal cmd |
        grep -v '_test\.go:' | grep -v ':0$' | awk -F: '{print $2 " " $1}' | sort -k2)
    expected=$(grep -v '^#' "$baseline" | grep -v '^$' | sort -k2)
    if [ "$current" = "$expected" ]; then
        return 0
    fi
    echo "warmup-check: raw reads of the local warmup column changed" >&2
    echo "  (count file; < baseline, > this tree)" >&2
    printf '%s\n' "$expected" >"${TMPDIR:-/tmp}/warmup-reads.expected.$$"
    printf '%s\n' "$current" >"${TMPDIR:-/tmp}/warmup-reads.current.$$"
    diff "${TMPDIR:-/tmp}/warmup-reads.expected.$$" "${TMPDIR:-/tmp}/warmup-reads.current.$$" >&2 || true
    rm -f "${TMPDIR:-/tmp}/warmup-reads.expected.$$" "${TMPDIR:-/tmp}/warmup-reads.current.$$"
    echo "Decide whether a mailbox is warming with models.ResolveEffectiveWarmup / Email.EffectiveWarmup," >&2
    echo "or warmupEnabledSQL / cloudWarmsSQL in SQL. Only code running this instance's own warmup" >&2
    echo "reads the column directly; record such a read in scripts/warmup-reads.baseline." >&2
    exit 1
}

main "$@"
