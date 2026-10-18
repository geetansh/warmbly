package validate

import (
	"testing"

	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
)

func TestMailboxUpdate(t *testing.T) {
	i := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	mode := func(v models.TestParticipationMode) *models.TestParticipationMode { return &v }
	for _, tc := range []struct {
		name string
		u    models.UpdateEmail
		want *errx.Error
	}{
		{"valid", models.UpdateEmail{Name: s("Sales"), WarmupBase: i(10), WarmupMax: i(40), WarmupDays: i(31), WarmupStartTime: s("08:00"), ReplyTo: s(" sales@example.com ")}, nil},
		{"base above max", models.UpdateEmail{WarmupBase: i(50), WarmupMax: i(40)}, errx.ErrEmailWarmupBase},
		{"base out of range", models.UpdateEmail{WarmupBase: i(101)}, errx.ErrEmailWarmupBase},
		{"max out of range", models.UpdateEmail{WarmupMax: i(-1)}, errx.ErrEmailWarmupMax},
		{"increase out of range", models.UpdateEmail{WarmupIncrease: i(101)}, errx.ErrEmailWarmupIncrease},
		{"reply rate out of range", models.UpdateEmail{WarmupReplyRate: i(101)}, errx.ErrEmailReplyRate},
		{"days out of range", models.UpdateEmail{WarmupDays: i(128)}, errx.ErrInvalid},
		{"tag not a slug", models.UpdateEmail{WarmupTag: s("two words")}, errx.ErrInvalid},
		{"status unknown", models.UpdateEmail{Status: s("paused")}, errx.ErrInvalid},
		{"campaign limit out of range", models.UpdateEmail{CampaignLimit: i(-1)}, errx.ErrEmailCampaignLimit},
		{"min wait out of range", models.UpdateEmail{MinWaitTime: i(86401)}, errx.ErrEmailMinWaitTime},
		{"reply-to not an address", models.UpdateEmail{ReplyTo: s("nobody")}, errx.ErrEmail},
		{"placement unknown", models.UpdateEmail{WarmupPlacement: s("drawer")}, errx.ErrEmailWarmupPlacement},
		{"test mode not settable", models.UpdateEmail{TestMode: mode(models.TestParticipationLegacy)}, errx.ErrNotEnough},
		{"negative shared limit", models.UpdateEmail{SharedDailyLimit: i(-1)}, errx.ErrNotEnough},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MailboxUpdate(&tc.u); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	u := models.UpdateEmail{ReplyTo: s(" sales@example.com "), WarmupTag: s(" SaaS ")}
	if MailboxUpdate(&u) != nil || *u.ReplyTo != " sales@example.com " || *u.WarmupTag != " SaaS " {
		t.Fatal("validation changed the patch it checked")
	}
	if MailboxUpdate(&models.UpdateEmail{WarmupStartTime: s("8am")}) == nil {
		t.Fatal("an unreadable start time passed")
	}
}
