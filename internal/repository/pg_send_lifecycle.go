package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/warmbly/warmbly/internal/infrastructure/db"
	"github.com/warmbly/warmbly/internal/models"
)

// SendLifecycleRepository reads and moves a mailbox's cold-sending lifecycle.
type SendLifecycleRepository interface {
	// GetSendLifecycles resolves a whole candidate pool in one round trip. The
	// campaign scheduler reads this per pass, so it must not be per-account.
	GetSendLifecycles(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]models.SendLifecycleState, error)
	// SetSendLifecycle moves one mailbox, stamping when and why. Refuses to
	// move a mailbox its owner put in reserve unless force is set, so the
	// rebalancer cannot override a deliberate hold.
	SetSendLifecycle(ctx context.Context, accountID uuid.UUID, state models.SendLifecycle, reason string, force bool) (bool, error)
	// ListLifecycleCandidates returns mailboxes the rebalancer may move, with
	// the warmup health that decides where they go, oldest-checked first so
	// every mailbox is reached rather than the same page every pass.
	ListLifecycleCandidates(ctx context.Context, limit int) ([]LifecycleCandidate, error)
	// GetLifecycleCandidate is ListLifecycleCandidates for one mailbox.
	GetLifecycleCandidate(ctx context.Context, accountID uuid.UUID) (*LifecycleCandidate, error)
	// MarkLifecycleChecked records that the rebalancer looked at these
	// mailboxes, which is what rotates the candidate window.
	MarkLifecycleChecked(ctx context.Context, accountIDs []uuid.UUID) error
	// RestartProbation re-stamps a resting mailbox's clock without changing
	// its state, so probation measures healthy time rather than time elapsed.
	RestartProbation(ctx context.Context, accountID uuid.UUID) error
}

// LifecycleCandidate is one mailbox the rebalancer is considering.
type LifecycleCandidate struct {
	EmailAccountID uuid.UUID
	Current        models.SendLifecycle
	Since          *time.Time
	// HealthState is empty when the mailbox is in no warmup pool, meaning no
	// signal rather than a healthy one. It is read from real evidence only.
	HealthState models.WarmupHealthState
	// CloudEvidenceUnknown is set while Warmbly Cloud has no current report for the mailbox.
	CloudEvidenceUnknown bool
}

type sendLifecycleRepository struct {
	DB *db.DB
}

func NewSendLifecycleRepository(database *db.DB) SendLifecycleRepository {
	return &sendLifecycleRepository{DB: database}
}

func (r *sendLifecycleRepository) GetSendLifecycles(ctx context.Context, accountIDs []uuid.UUID) (map[uuid.UUID]models.SendLifecycleState, error) {
	out := make(map[uuid.UUID]models.SendLifecycleState, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := r.DB.Pool.Query(ctx, `
		SELECT id, send_lifecycle, send_lifecycle_since, send_lifecycle_reason
		  FROM email_accounts WHERE id = ANY($1::uuid[])
	`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var state string
		var since *time.Time
		var reason *string
		if err := rows.Scan(&id, &state, &since, &reason); err != nil {
			return nil, err
		}
		s := models.SendLifecycleState{State: models.SendLifecycle(state), Since: since}
		if reason != nil {
			s.Reason = *reason
		}
		out[id] = s
	}
	return out, rows.Err()
}

func (r *sendLifecycleRepository) SetSendLifecycle(ctx context.Context, accountID uuid.UUID, state models.SendLifecycle, reason string, force bool) (bool, error) {
	// The guard is in SQL so a concurrent owner setting reserve cannot be
	// overwritten between a read and a write.
	tag, err := r.DB.Pool.Exec(ctx, `
		UPDATE email_accounts
		   SET send_lifecycle = $2,
		       send_lifecycle_since = NOW(),
		       send_lifecycle_reason = NULLIF($3, '')
		 WHERE id = $1
		   AND send_lifecycle <> $2
		   AND ($4 OR send_lifecycle <> 'reserve')
	`, accountID, string(state), reason, force)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *sendLifecycleRepository) ListLifecycleCandidates(ctx context.Context, limit int) ([]LifecycleCandidate, error) {
	if limit <= 0 {
		limit = 500
	}
	// health_state stays NULL for a mailbox with no standing: leaving the pool
	// is not evidence of health, so the caller must see "unknown" rather than a
	// clean bill that would auto-resume it.
	rows, err := r.DB.Pool.Query(ctx, `
		SELECT ea.id, ea.send_lifecycle, ea.send_lifecycle_since,
		       wh.health_state, `+cloudStandingUnknownSQL("ea.id")+`
		  FROM email_accounts ea
		  LEFT JOIN LATERAL (`+warmupKnownStandingSQL("ea.id")+`
		  ) wh ON true
		 WHERE ea.status = 'active'
		   AND ea.send_lifecycle IN ('active', 'resting')
		 ORDER BY ea.send_lifecycle_checked_at NULLS FIRST, ea.id
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LifecycleCandidate
	for rows.Next() {
		var c LifecycleCandidate
		var state string
		var health *string
		if err := rows.Scan(&c.EmailAccountID, &state, &c.Since, &health, &c.CloudEvidenceUnknown); err != nil {
			return nil, err
		}
		c.Current = models.SendLifecycle(state)
		if health != nil {
			c.HealthState = models.WarmupHealthState(*health)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *sendLifecycleRepository) GetLifecycleCandidate(ctx context.Context, accountID uuid.UUID) (*LifecycleCandidate, error) {
	var c LifecycleCandidate
	var state string
	var health *string
	err := r.DB.Pool.QueryRow(ctx, `
		SELECT ea.id, ea.send_lifecycle, ea.send_lifecycle_since, wh.health_state,
		       `+cloudStandingUnknownSQL("ea.id")+`
		  FROM email_accounts ea
		  LEFT JOIN LATERAL (`+warmupKnownStandingSQL("ea.id")+`
		  ) wh ON true
		 WHERE ea.id = $1
	`, accountID).Scan(&c.EmailAccountID, &state, &c.Since, &health, &c.CloudEvidenceUnknown)
	if err != nil {
		return nil, err
	}
	c.Current = models.SendLifecycle(state)
	if health != nil {
		c.HealthState = models.WarmupHealthState(*health)
	}
	return &c, nil
}

func (r *sendLifecycleRepository) MarkLifecycleChecked(ctx context.Context, accountIDs []uuid.UUID) error {
	if len(accountIDs) == 0 {
		return nil
	}
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE email_accounts SET send_lifecycle_checked_at = NOW() WHERE id = ANY($1::uuid[])`, accountIDs)
	return err
}

func (r *sendLifecycleRepository) RestartProbation(ctx context.Context, accountID uuid.UUID) error {
	_, err := r.DB.Pool.Exec(ctx, `
		UPDATE email_accounts
		   SET send_lifecycle_since = NOW()
		 WHERE id = $1 AND send_lifecycle = 'resting'
	`, accountID)
	return err
}
