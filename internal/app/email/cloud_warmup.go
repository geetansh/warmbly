package email

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/utils/validate"
)

// CloudWarmupRelay sends the warmup changes Warmbly Cloud owns to it; satisfied by the cloudlink service.
type CloudWarmupRelay interface {
	RelayWarmupSettings(ctx context.Context, orgID, accountID uuid.UUID, want models.PoolLinkWarmupSettings, lifecycle string) (*models.PoolLinkWarmupSettings, *errx.Error)
	SetLifecycle(ctx context.Context, orgID, accountID uuid.UUID, action string) (*models.CloudLinkMailboxRow, *errx.Error)
}

// WireCloudWarmup routes warmup edits for Cloud-warmed mailboxes to Warmbly Cloud.
func (s *emailService) WireCloudWarmup(r CloudWarmupRelay) {
	s.cloudWarmup = r
}

var (
	ErrCloudWarmupValueUnsupported = errx.NewWithIdentifier(errx.Unprocessable, "cloud_warmup_value_unsupported",
		"Warmbly Cloud warms this mailbox and cannot take 0 for this value from here. Use 1 or more, or change it in Warmbly Cloud.")
	ErrCloudWarmupManaged = errx.NewWithIdentifier(errx.Conflict, "cloud_warmup_managed",
		"Warmbly Cloud warms this mailbox, so this is changed through its Warmbly Cloud controls: pause or resume it there, set its participation there, or remove it from Warmbly Cloud to stop warmup.")
)

// cloudOwned returns the enrollment whose warmup settings belong to Warmbly Cloud, nil when they are still this instance's.
func (s *emailService) cloudOwned(ctx context.Context, accountID uuid.UUID) (*models.CloudLinkMailbox, *errx.Error) {
	if s.cloudWarmup == nil || s.cloudLink == nil {
		return nil, nil
	}
	m, err := s.cloudLink.GetByAccount(ctx, accountID)
	if err != nil {
		return nil, errx.InternalError()
	}
	// A first enrollment still in flight proposes the local values when it completes.
	if m == nil || m.EnrollmentState == "pending_remove" || m.EnrollmentState == "pending_enroll" && m.SettingsObservedAt == nil {
		return nil, nil
	}
	return m, nil
}

func touchesCloudWarmup(u *models.UpdateEmail) bool {
	return u.Warmup != nil || u.WarmupBase != nil || u.WarmupMax != nil || u.WarmupIncrease != nil || u.WarmupReplyRate != nil ||
		u.WarmupStartTime != nil || u.WarmupEndTime != nil || u.WarmupDays != nil ||
		u.TestMode != nil || u.TestSendEnabled != nil || u.TestReceiveEnabled != nil || u.SharedDailyLimit != nil || u.RollingRecipientLimit != nil
}

// relayCloudWarmup sends the patch's Cloud-owned fields to Warmbly Cloud and removes them from it; the rest is written locally.
func (s *emailService) relayCloudWarmup(ctx context.Context, orgID, emailAccountID string, u *models.UpdateEmail) (bool, *errx.Error) {
	if s.cloudWarmup == nil || !touchesCloudWarmup(u) {
		return false, nil
	}
	// Refused before anything reaches the cloud, so a bad field cannot leave a change half applied.
	if xerr := validate.MailboxUpdate(u); xerr != nil {
		return false, xerr
	}
	account, xerr := s.emailRepository.Get(ctx, orgID, emailAccountID)
	if xerr != nil {
		return false, xerr
	}
	if account.OrganizationID == nil {
		return false, errx.ErrNotFound
	}
	m, xerr := s.cloudOwned(ctx, account.ID)
	if xerr != nil || m == nil {
		return false, xerr
	}
	if u.TestMode != nil || u.TestSendEnabled != nil || u.TestReceiveEnabled != nil || u.SharedDailyLimit != nil || u.RollingRecipientLimit != nil {
		return false, ErrCloudWarmupManaged
	}
	if u.WarmupStartTime != nil || u.WarmupEndTime != nil {
		start, end := account.WarmupStartTime, account.WarmupEndTime
		if u.WarmupStartTime != nil {
			start = *u.WarmupStartTime
		}
		if u.WarmupEndTime != nil {
			end = *u.WarmupEndTime
		}
		if xerr := validate.WarmupWindow(start, end); xerr != nil {
			return false, xerr
		}
	}
	var want models.PoolLinkWarmupSettings
	for _, f := range []struct {
		value   **int
		current int
		dst     *int
	}{
		{&u.WarmupBase, account.WarmupBase, &want.Base}, {&u.WarmupMax, account.WarmupMax, &want.Max},
		{&u.WarmupIncrease, account.WarmupIncrease, &want.Increase}, {&u.WarmupReplyRate, account.WarmupReplyRate, &want.ReplyRate},
	} {
		if *f.value == nil {
			continue
		}
		if **f.value == 0 {
			if f.current != 0 {
				return false, ErrCloudWarmupValueUnsupported
			}
			continue // Already 0 on the cloud's copy; nothing to send.
		}
		*f.dst = **f.value
	}
	if u.WarmupStartTime != nil {
		want.StartTime = models.ClockHHMM(*u.WarmupStartTime)
	}
	if u.WarmupEndTime != nil {
		want.EndTime = models.ClockHHMM(*u.WarmupEndTime)
	}
	if u.WarmupDays != nil {
		want.Days = *u.WarmupDays
		if want.Days == 0 {
			want.Days = models.WarmupEveryDay
		}
	}
	lifecycle := ""
	if u.Warmup != nil {
		lifecycle = "pause"
		if *u.Warmup {
			lifecycle = "resume"
		}
	}
	if want == (models.PoolLinkWarmupSettings{}) && lifecycle == "" {
		clearCloudWarmup(u)
		return true, nil
	}
	// The timezone stays this instance's; it travels with a Cloud change so the window is read in it.
	if u.Timezone != nil {
		if want.Timezone = strings.TrimSpace(*u.Timezone); want.Timezone == "" {
			want.Timezone = account.OrgTimezone
		}
	}
	if _, xerr := s.cloudWarmup.RelayWarmupSettings(ctx, *account.OrganizationID, account.ID, want, lifecycle); xerr != nil {
		return false, xerr
	}
	clearCloudWarmup(u)
	return true, nil
}

// clearCloudWarmup drops the fields the cloud now holds, so the local write cannot overwrite its mirror.
func clearCloudWarmup(u *models.UpdateEmail) {
	u.Warmup, u.WarmupBase, u.WarmupMax, u.WarmupIncrease, u.WarmupReplyRate = nil, nil, nil, nil, nil
	u.WarmupStartTime, u.WarmupEndTime, u.WarmupDays = nil, nil, nil
}

// cloudLifecycle relays a warmup lifecycle action for a Cloud-warmed mailbox; handled is false when the instance owns it.
func (s *emailService) cloudLifecycle(ctx context.Context, orgID, emailAccountID, action string) (*models.Email, bool, *errx.Error) {
	if s.cloudWarmup == nil {
		return nil, false, nil
	}
	account, xerr := s.emailRepository.Get(ctx, orgID, emailAccountID)
	if xerr != nil {
		return nil, false, xerr
	}
	m, xerr := s.cloudOwned(ctx, account.ID)
	if xerr != nil || m == nil || account.OrganizationID == nil {
		return nil, false, xerr
	}
	switch action {
	case "start", "resume":
		action = "resume"
	case "pause":
	default:
		return nil, true, ErrCloudWarmupManaged
	}
	if _, xerr := s.cloudWarmup.SetLifecycle(ctx, *account.OrganizationID, account.ID, action); xerr != nil {
		return nil, true, xerr
	}
	account, xerr = s.emailRepository.Get(ctx, orgID, emailAccountID)
	return account, true, xerr
}
