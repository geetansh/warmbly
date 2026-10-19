package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/warmbly/warmbly/internal/models"
)

// warmupStandingRankSQL orders health states worst first when sorted DESC.
func warmupStandingRankSQL(col string) string {
	return `CASE ` + col + `
			WHEN 'blocked' THEN 5
			WHEN 'quarantined' THEN 4
			WHEN 'throttled' THEN 3
			WHEN 'watch' THEN 2
			WHEN 'healthy' THEN 1
			ELSE 0
		END`
}

// cloudReportMissingPredicate: the clm row has no current, recognized standing.
const cloudReportMissingPredicate = `(clm.enrollment_state <> 'active' OR EXISTS (SELECT 1 FROM cloud_link WHERE instance_id = clm.instance_id AND disconnect_pending)
	 OR clm.health_state IS NULL OR clm.health_state NOT IN ('healthy', 'watch', 'throttled', 'quarantined', 'blocked') OR clm.standing_observed_at IS NULL
	 OR clm.standing_observed_at <= NOW() - INTERVAL '15 minutes' OR clm.standing_observed_at > NOW())`

// cloudStandingUnknownPredicate: the clm row has no current, recognized standing and no live hold.
const cloudStandingUnknownPredicate = `(` + cloudReportMissingPredicate + `
	 AND NOT (COALESCE(clm.health_state IN ('quarantined', 'blocked'), false)
	 AND (clm.blocked_until IS NULL OR clm.blocked_until > NOW())))`

// warmupStandingSQL is a mailbox's worst warmup standing, at most one row:
// its local pool row, or the standing Warmbly Cloud last reported for a
// mailbox it warms (which has no local pool row). A standing past its term
// ranks below any live one, so it cannot mask one. Unknown cloud evidence
// reads as blocked, so the gates that use this fail closed.
// Columns: health_state, blocked_until, last_health_score,
// last_health_reason, last_health_evaluated_at.
func warmupStandingSQL(accountExpr string) string {
	return warmupStandingUnionSQL(accountExpr, false)
}

// warmupKnownStandingSQL is warmupStandingSQL without the row unknown cloud evidence synthesises.
func warmupKnownStandingSQL(accountExpr string) string {
	return warmupStandingUnionSQL(accountExpr, true)
}

// cloudReportMissingSQL is a boolean: Warmbly Cloud has no current report for the mailbox, held or not.
func cloudReportMissingSQL(accountExpr string) string {
	return `EXISTS (SELECT 1 FROM cloud_link_mailboxes clm WHERE clm.email_account_id = ` + accountExpr + ` AND ` + cloudReportMissingPredicate + `)`
}

// cloudStandingUnknownSQL is a boolean: the mailbox's Warmbly Cloud evidence is unknown.
func cloudStandingUnknownSQL(accountExpr string) string {
	return `EXISTS (SELECT 1 FROM cloud_link_mailboxes clm WHERE clm.email_account_id = ` + accountExpr + ` AND ` + cloudStandingUnknownPredicate + `)`
}

// liveHoldSQL is a boolean: the standing row aliased alias is a quarantine or block still in term.
func liveHoldSQL(alias string) string {
	return `COALESCE(` + alias + `.health_state IN ('quarantined', 'blocked') AND (` + alias + `.blocked_until IS NULL OR ` + alias + `.blocked_until > NOW()), false)`
}

// warmupGateStandingSQL is the send gate's standing: a live real hold's state, else warmupStandingSQL's row.
// Columns: health_state, blocked_until, cloud_only (held only by a missing Cloud report), cloud_missing.
func warmupGateStandingSQL(accountExpr string) string {
	// A missing Cloud report has no end, so any hold beside it is open-ended at the gate.
	return `
		SELECT CASE WHEN ` + liveHoldSQL("k") + ` THEN k.health_state ELSE h.health_state END AS health_state,
		       CASE WHEN u.missing THEN NULL WHEN ` + liveHoldSQL("k") + ` THEN k.blocked_until ELSE h.blocked_until END AS blocked_until,
		       (u.unknown AND NOT ` + liveHoldSQL("k") + `) AS cloud_only,
		       u.missing AS cloud_missing
		  FROM (` + warmupStandingSQL(accountExpr) + `) h
		  LEFT JOIN LATERAL (` + warmupKnownStandingSQL(accountExpr) + `) k ON true
		  CROSS JOIN LATERAL (SELECT ` + cloudStandingUnknownSQL(accountExpr) + ` AS unknown, ` + cloudReportMissingSQL(accountExpr) + ` AS missing) u`
}

// cloudStatusSelect is the columns cloudStatusRow scans, over a warmupKnownStandingSQL lateral aliased k.
func cloudStatusSelect(accountExpr string) string {
	return cloudStandingUnknownSQL(accountExpr) + `, k.health_state, k.blocked_until, k.last_health_score, k.last_health_reason, k.last_health_evaluated_at`
}

type cloudStatusRow struct {
	unknown     bool
	state       *string
	until       *time.Time
	score       *float64
	reason      *string
	evaluatedAt *time.Time
}

func (c *cloudStatusRow) dest() []any {
	return []any{&c.unknown, &c.state, &c.until, &c.score, &c.reason, &c.evaluatedAt}
}

// status is what a Cloud-warmed mailbox shows: a live hold first, then unknown evidence, then Cloud's report.
func (c cloudStatusRow) status(m *models.CloudLinkMailbox, now time.Time) *models.WarmupHealthInfo {
	hold := c.state != nil && models.WarmupHealthState(*c.state).Holds() && (c.until == nil || c.until.After(now))
	// Cloud's own report stands only when it is that same live row; it carries the pool and partner detail.
	same := !c.unknown && m.Standing != nil && hold && m.Standing.State == *c.state && sameMicrosecond(m.Standing.BlockedUntil, c.until)
	if hold && !same {
		info := &models.WarmupHealthInfo{Source: models.WarmupHealthSourceCloud, State: *c.state, BlockedUntil: c.until, EvaluatedAt: c.evaluatedAt}
		if c.score != nil {
			info.Score = *c.score
		}
		if c.reason != nil {
			info.Reason = *c.reason
		}
		return info
	}
	if c.unknown {
		return models.CloudStandingUnavailable()
	}
	return m.Standing
}

// sameMicrosecond compares two optional times at the database's microsecond precision.
func sameMicrosecond(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

// cloudStatuses is what each Cloud-warmed mailbox in linked shows; absent when there is nothing to show.
func cloudStatuses(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, linked map[uuid.UUID]*models.CloudLinkMailbox) (map[uuid.UUID]*models.WarmupHealthInfo, error) {
	out := make(map[uuid.UUID]*models.WarmupHealthInfo, len(linked))
	if len(linked) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(linked))
	for id := range linked {
		ids = append(ids, id)
	}
	rows, err := q.Query(ctx, `
		SELECT a.id, `+cloudStatusSelect("a.id")+`
		  FROM unnest($1::uuid[]) AS a(id)
		  LEFT JOIN LATERAL (`+warmupKnownStandingSQL("a.id")+`
		  ) k ON true`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var id uuid.UUID
		var c cloudStatusRow
		if err := rows.Scan(append([]any{&id}, c.dest()...)...); err != nil {
			return nil, err
		}
		if st := c.status(linked[id], now); st != nil {
			out[id] = st
		}
	}
	return out, rows.Err()
}

func warmupStandingUnionSQL(accountExpr string, knownOnly bool) string {
	unknown := cloudStandingUnknownPredicate
	cloudFilter := ""
	if knownOnly {
		cloudFilter = ` AND NOT COALESCE(` + unknown + `, false)`
	}
	return `
		SELECT st.health_state, st.blocked_until, st.last_health_score,
		       st.last_health_reason, st.last_health_evaluated_at
		  FROM (
		        SELECT wpp.health_state::text AS health_state, wpp.blocked_until,
		               wpp.last_health_score, wpp.last_health_reason, wpp.last_health_evaluated_at
		          FROM warmup_pool_participants wpp
		         WHERE wpp.email_account_id = ` + accountExpr + `
		        UNION ALL
		        SELECT CASE WHEN ` + unknown + ` THEN 'blocked' ELSE clm.health_state END,
		               CASE WHEN ` + unknown + ` THEN NULL ELSE clm.blocked_until END,
		               clm.health_score, CASE WHEN ` + unknown + ` THEN '` + models.WarmupReasonCloudEvidenceUnavailable + `' ELSE clm.health_reason END,
		               clm.health_evaluated_at
		          FROM cloud_link_mailboxes clm
		         WHERE clm.email_account_id = ` + accountExpr + cloudFilter + `
		        UNION ALL
		        SELECT l.cloud_health_state, l.cloud_blocked_until,
		               l.cloud_health_score, l.cloud_health_reason, NULL::timestamptz
		          FROM warmup_reputation_ledger l JOIN email_accounts ledger_account
		            ON ledger_account.organization_id = l.organization_id AND lower(btrim(ledger_account.email)) = l.email
		         WHERE ledger_account.id = ` + accountExpr + ` AND l.cloud_health_state IS NOT NULL
		           AND (l.cloud_blocked_until IS NULL OR l.cloud_blocked_until > NOW())
		       ) st
		 ORDER BY COALESCE(st.blocked_until <= NOW(), false) ASC,
		          ` + warmupStandingRankSQL("st.health_state") + ` DESC,
		          (st.health_state = 'blocked' AND st.blocked_until IS NULL) DESC,
		          st.blocked_until DESC NULLS LAST
		 LIMIT 1`
}
