package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/warmbly/warmbly/internal/models"
)

// SetEmailAccountRiskBand records a mailbox's classification along with the
// timestamp it was evaluated.
func (r *workerRepository) SetEmailAccountRiskBand(ctx context.Context, emailAccountID uuid.UUID, band models.EmailRiskBand) error {
	_, err := r.db.Exec(ctx, `
		UPDATE email_accounts
		SET risk_band = $2, risk_evaluated_at = NOW()
		WHERE id = $1
	`, emailAccountID, band)
	return err
}

// GetEmailAccountRiskBand returns the stored risk_band for a mailbox. A
// missing row, or a NULL band (shouldn't happen - the column is NOT NULL with
// a 'clean' default), is treated as clean: assume innocent until the warmup
// health sweep proves otherwise.
func (r *workerRepository) GetEmailAccountRiskBand(ctx context.Context, emailAccountID uuid.UUID) (models.EmailRiskBand, error) {
	var band models.EmailRiskBand
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(risk_band, 'clean'::email_risk_band)
		FROM email_accounts
		WHERE id = $1
	`, emailAccountID).Scan(&band)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EmailRiskBandClean, nil
	}
	if err != nil {
		return models.EmailRiskBandClean, err
	}
	return band, nil
}

// RiskCandidate is one mailbox's current risk band alongside the warmup health
// state the new band is derived from.
type RiskCandidate struct {
	EmailAccountID uuid.UUID
	UserID         uuid.UUID
	OrgID          *uuid.UUID
	CurrentBand    models.EmailRiskBand
	HealthState    models.WarmupHealthState // may be empty if no health row; real evidence only
	// CloudEvidenceUnknown is set while Warmbly Cloud has no current report for the mailbox.
	CloudEvidenceUnknown bool
	WorkerID             *uuid.UUID
	LastEvaluated        *time.Time
}

// ListRiskCandidates joins email_accounts with warmup health so the risk sweep
// can compute each mailbox's new band in one scan. The band feeds warmup
// partner selection and pacing; it no longer moves mailboxes between workers,
// because which machine a mailbox sends from is not what recipient filters
// judge it on.
func (r *workerRepository) ListRiskCandidates(ctx context.Context, limit int) ([]RiskCandidate, error) {
	if limit <= 0 {
		limit = 500
	}
	// Health is the mailbox's standing: its pool row, or what Warmbly Cloud
	// reported for a mailbox it warms.
	rows, err := r.db.Query(ctx, `
		SELECT
			ea.id,
			ea.user_id,
			ea.organization_id,
			ea.risk_band,
			COALESCE(wh.health_state, '')::text,
			`+cloudStandingUnknownSQL("ea.id")+`,
			ea.worker_id,
			ea.risk_evaluated_at
		FROM email_accounts ea
		LEFT JOIN LATERAL (`+warmupKnownStandingSQL("ea.id")+`
		) wh ON true
		WHERE ea.status = 'active'
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RiskCandidate
	for rows.Next() {
		var c RiskCandidate
		var hs string
		if err := rows.Scan(
			&c.EmailAccountID,
			&c.UserID,
			&c.OrgID,
			&c.CurrentBand,
			&hs,
			&c.CloudEvidenceUnknown,
			&c.WorkerID,
			&c.LastEvaluated,
		); err != nil {
			return nil, err
		}
		c.HealthState = models.WarmupHealthState(hs)
		out = append(out, c)
	}
	return out, rows.Err()
}
