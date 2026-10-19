package repository

import (
	"time"

	"github.com/warmbly/warmbly/internal/models"
)

// cloudLinkWarmupJoin and cloudLinkWarmupColumns read a mailbox's Warmbly Cloud enrollment with its row; group by clw.email_account_id too.
const (
	cloudLinkWarmupJoin    = `LEFT JOIN cloud_link_mailboxes clw ON clw.email_account_id = ea.id`
	cloudLinkWarmupColumns = `clw.enrollment_state, clw.cloud_warmup_state, clw.cloud_warmup_started_at, clw.cloud_warmup_volume, clw.cloud_warmup_target, clw.cloud_warmup_observed_at`
)

// cloudWarmsSQL is true where Warmbly Cloud, not this instance, warms the mailbox; it needs cloudLinkWarmupJoin. Mirrors CloudLinkWarmup.Warms.
const cloudWarmsSQL = `(clw.enrollment_state IS NOT NULL AND clw.enrollment_state <> 'pending_remove')`

// warmupEnabledSQL is true where warmup is on or paused for the email_accounts row aliased as, here or in Warmbly Cloud; unreported Cloud warmup counts as not enabled.
func warmupEnabledSQL(as string) string {
	return `COALESCE((SELECT clw.cloud_warmup_state IN ('active', 'paused') FROM cloud_link_mailboxes clw
		WHERE clw.email_account_id = ` + as + `.id AND clw.enrollment_state <> 'pending_remove'), ` + as + `.warmup IS NOT NULL)`
}

type cloudLinkWarmupScan struct {
	enrollment *string
	state      *string
	startedAt  *time.Time
	volume     *int
	target     *int
	observedAt *time.Time
}

// apply sets the enrollment on e and resolves its effective warmup.
func (c *cloudLinkWarmupScan) apply(e *models.Email) {
	if c.enrollment != nil {
		cw := &models.CloudLinkWarmup{EnrollmentState: *c.enrollment, StartedAt: c.startedAt, Volume: c.volume, Target: c.target, ObservedAt: c.observedAt}
		if c.state != nil {
			st := models.WarmupState(*c.state)
			cw.State = &st
		}
		e.CloudWarmup = cw
	}
	w := e.EffectiveWarmup()
	e.WarmupEffective = &w
}
