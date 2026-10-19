package validate

import (
	"strings"
	"unicode/utf8"

	"github.com/warmbly/warmbly/internal/config"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
)

// MailboxUpdate checks a mailbox patch without changing it, so a caller can refuse it before any side effect.
func MailboxUpdate(u *models.UpdateEmail) *errx.Error {
	if u.Name != nil {
		if !EmailName(u.Name) {
			return errx.ErrEmailName
		}
		if xerr := MailboxNameShown(*u.Name); xerr != nil {
			return xerr
		}
	}
	// Characters, not bytes: the documented limit is in characters.
	if u.SignaturePlain != nil && utf8.RuneCountInString(*u.SignaturePlain) > config.SignaturePlainMax {
		return errx.ErrEmailSignaturePlain
	}
	if u.SignatureHTML != nil && utf8.RuneCountInString(*u.SignatureHTML) > config.SignatureHTMLMax {
		return errx.ErrEmailSignatureHTML
	}
	if u.Timezone != nil {
		if xerr := EmailTimezone(strings.TrimSpace(*u.Timezone)); xerr != nil {
			return xerr
		}
	}
	if u.Status != nil && *u.Status != "active" && *u.Status != "inactive" && *u.Status != "revoked" {
		return errx.ErrInvalid
	}
	if u.CampaignLimit != nil && (*u.CampaignLimit < config.LimitMin || *u.CampaignLimit > config.LimitMax) {
		return errx.ErrEmailCampaignLimit
	}
	if u.MinWaitTime != nil && (*u.MinWaitTime < 0 || *u.MinWaitTime > 86400) {
		return errx.ErrEmailMinWaitTime
	}
	if u.ReplyTo != nil {
		if to := strings.TrimSpace(*u.ReplyTo); to != "" && !Email(to) {
			return errx.ErrEmail
		}
	}
	// Starting volume must not exceed the ceiling when both arrive together.
	if u.WarmupBase != nil && u.WarmupMax != nil && *u.WarmupBase > *u.WarmupMax {
		return errx.ErrEmailWarmupBase
	}
	for _, f := range []struct {
		value *int
		err   *errx.Error
	}{
		{u.WarmupBase, errx.ErrEmailWarmupBase}, {u.WarmupMax, errx.ErrEmailWarmupMax},
		{u.WarmupIncrease, errx.ErrEmailWarmupIncrease}, {u.WarmupReplyRate, errx.ErrEmailReplyRate},
	} {
		if f.value != nil && (*f.value < 0 || *f.value > 100) {
			return f.err
		}
	}
	if u.WarmupTag != nil {
		// The content segment is a lowercase slug the AI content bank is keyed on.
		seg := strings.ToLower(strings.TrimSpace(*u.WarmupTag))
		if len(seg) > 32 {
			return errx.ErrInvalid
		}
		for _, r := range seg {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				return errx.ErrInvalid
			}
		}
	}
	for _, t := range []*string{u.WarmupStartTime, u.WarmupEndTime} {
		if t != nil {
			if xerr := CampaignTime(*t); xerr != nil {
				return xerr
			}
		}
	}
	if u.WarmupDays != nil && (*u.WarmupDays < 0 || *u.WarmupDays > 127) {
		return errx.ErrInvalid
	}
	if u.WarmupPlacement != nil && !models.ValidWarmupPlacement(*u.WarmupPlacement) {
		return errx.ErrEmailWarmupPlacement
	}
	if u.WarmupFolder != nil {
		// The value becomes a single-level, printable mailbox name on the provider.
		if xerr := WarmupFolder(strings.TrimSpace(*u.WarmupFolder)); xerr != nil {
			return xerr
		}
	}
	if u.WarmupRetentionDays != nil && !models.ValidWarmupRetentionDays(*u.WarmupRetentionDays) {
		return errx.ErrEmailWarmupRetention
	}
	if u.TestMode != nil && *u.TestMode != models.TestParticipationDiagnostic && *u.TestMode != models.TestParticipationOff {
		return errx.ErrNotEnough
	}
	for _, limit := range []*int{u.SharedDailyLimit, u.RollingRecipientLimit} {
		if limit != nil && *limit < 0 {
			return errx.ErrNotEnough
		}
	}
	return nil
}

// WarmupWindow refuses a warmup window that does not end later than it starts on the same day.
func WarmupWindow(start, end string) *errx.Error {
	startMinute, endMinute := models.ClockMinutes(start, -1), models.ClockMinutes(end, -1)
	if startMinute < 0 || endMinute < 0 || endMinute <= startMinute {
		return errx.New(errx.BadRequest, "warmup end time must be later than start time on the same day")
	}
	return nil
}
