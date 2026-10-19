package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/infrastructure/db"
	"github.com/warmbly/warmbly/internal/models"
)

// CloudWarmupMirrorRepository keeps an enrolled mailbox's warmup columns equal to what Warmbly Cloud reports.
type CloudWarmupMirrorRepository interface {
	// MirrorCloudWarmupSettings writes settings observed at observedAt and returns the mailboxes whose columns changed.
	MirrorCloudWarmupSettings(ctx context.Context, observedAt time.Time, settings map[uuid.UUID]models.PoolLinkWarmupSettings) ([]uuid.UUID, error)
}

var _ CloudWarmupMirrorRepository = (*cloudLinkRepository)(nil)

// CloudWarmupStateRepository keeps the warmup Warmbly Cloud last reported for each mailbox it warms.
type CloudWarmupStateRepository interface {
	// MirrorCloudWarmupState records states observed at observedAt and returns the mailboxes whose state changed.
	MirrorCloudWarmupState(ctx context.Context, observedAt time.Time, states map[uuid.UUID]models.CloudWarmupObserved) ([]uuid.UUID, error)
	// ForgetCloudWarmupState marks a mailbox the Cloud no longer lists as unreported.
	ForgetCloudWarmupState(ctx context.Context, accountID uuid.UUID) error
}

var _ CloudWarmupStateRepository = (*cloudLinkRepository)(nil)

func (r *cloudLinkRepository) MirrorCloudWarmupState(ctx context.Context, observedAt time.Time, states map[uuid.UUID]models.CloudWarmupObserved) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	var kinds []string
	var started []*time.Time
	var volumes, targets []*int32
	for id, s := range states {
		switch s.State {
		case models.WarmupStateActive, models.WarmupStatePaused, models.WarmupStateOff:
		default:
			continue
		}
		ids, kinds, started = append(ids, id), append(kinds, string(s.State)), append(started, s.StartedAt)
		volumes, targets = append(volumes, int32Ptr(s.Volume)), append(targets, int32Ptr(s.Target))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// The state column is compared alone, so a volume tick is stored without announcing a change.
	query := `
		WITH s AS (
			SELECT * FROM unnest($2::uuid[], $3::text[], $4::timestamptz[], $5::int[], $6::int[])
			       AS s(id, state, started_at, volume, target)
		), prior AS (
			SELECT c.email_account_id, c.cloud_warmup_state
			  FROM cloud_link_mailboxes c JOIN s ON s.id = c.email_account_id
		)
		UPDATE cloud_link_mailboxes c
		   SET cloud_warmup_state = s.state, cloud_warmup_started_at = s.started_at,
		       cloud_warmup_volume = s.volume, cloud_warmup_target = s.target, cloud_warmup_observed_at = $1
		  FROM s JOIN prior p ON p.email_account_id = s.id
		 WHERE c.email_account_id = s.id AND c.enrollment_state <> 'pending_remove'
		   AND (c.cloud_warmup_observed_at IS NULL OR c.cloud_warmup_observed_at < $1)
		RETURNING c.email_account_id, p.cloud_warmup_state IS DISTINCT FROM s.state`
	rows, err := r.db.Query(ctx, query, observedAt, ids, kinds, started, volumes, targets)
	if err != nil {
		db.CaptureError(err, query, nil, "query")
		return nil, err
	}
	defer rows.Close()
	var changed []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var moved bool
		if err := rows.Scan(&id, &moved); err != nil {
			return nil, err
		}
		if moved {
			changed = append(changed, id)
		}
	}
	return changed, rows.Err()
}

func (r *cloudLinkRepository) ForgetCloudWarmupState(ctx context.Context, accountID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE cloud_link_mailboxes
		   SET cloud_warmup_state = NULL, cloud_warmup_volume = NULL, cloud_warmup_target = NULL, cloud_warmup_observed_at = NULL
		 WHERE email_account_id = $1 AND cloud_warmup_state IS NOT NULL`, accountID)
	return err
}

func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v)
	return &n
}

func mirrorable(w models.PoolLinkWarmupSettings) bool {
	for _, v := range []int{w.Base, w.Max, w.Increase, w.ReplyRate} {
		if v < 0 || v > 100 {
			return false
		}
	}
	return w.Days >= 0 && w.Days <= models.WarmupEveryDay && w.StartTime != "" && w.EndTime != ""
}

func (r *cloudLinkRepository) MirrorCloudWarmupSettings(ctx context.Context, observedAt time.Time, settings map[uuid.UUID]models.PoolLinkWarmupSettings) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	var bases, maxes, increases, replies, days []int32
	var starts, ends []string
	for id, w := range settings {
		w = w.Mirrored()
		if !mirrorable(w) {
			log.Warn().Str("account_id", id.String()).Msg("cloud link: reported warmup settings are out of range; local copy left as it is")
			continue
		}
		ids = append(ids, id)
		bases, maxes, increases, replies = append(bases, int32(w.Base)), append(maxes, int32(w.Max)), append(increases, int32(w.Increase)), append(replies, int32(w.ReplyRate))
		starts, ends, days = append(starts, w.StartTime), append(ends, w.EndTime), append(days, int32(w.Days))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// observedAt orders a slow listing behind a newer relay without holding the reconciliation lock.
	query := `
		UPDATE email_accounts ea
		   SET warmup_base = s.base, warmup_max = s.max, warmup_increase = s.inc, warmup_reply_rate = s.reply,
		       warmup_start_time = s.start_at::time, warmup_end_time = s.end_at::time, warmup_days = s.days, updated_at = now()
		  FROM unnest($2::uuid[], $3::int[], $4::int[], $5::int[], $6::smallint[], $7::text[], $8::text[], $9::smallint[])
		       AS s(id, base, max, inc, reply, start_at, end_at, days)
		  JOIN cloud_link_mailboxes c ON c.email_account_id = s.id
		 WHERE ea.id = s.id AND c.enrollment_state <> 'pending_remove'
		   AND (c.settings_observed_at IS NULL OR c.settings_observed_at < $1)
		   AND (ea.warmup_base, ea.warmup_max, ea.warmup_increase, ea.warmup_reply_rate, ea.warmup_start_time, ea.warmup_end_time, ea.warmup_days)
		       IS DISTINCT FROM (s.base, s.max, s.inc, s.reply, s.start_at::time, s.end_at::time, s.days)
		RETURNING ea.id`
	rows, err := tx.Query(ctx, query, observedAt, ids, bases, maxes, increases, replies, starts, ends, days)
	if err != nil {
		db.CaptureError(err, query, nil, "query")
		return nil, err
	}
	var changed []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		changed = append(changed, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cloud_link_mailboxes SET settings_observed_at = $1
		 WHERE email_account_id = ANY($2::uuid[]) AND enrollment_state <> 'pending_remove'
		   AND (settings_observed_at IS NULL OR settings_observed_at < $1)`, observedAt, ids); err != nil {
		return nil, err
	}
	return changed, tx.Commit(ctx)
}
