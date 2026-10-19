package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/app/analytics"
	"github.com/warmbly/warmbly/internal/app/email"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
)

type mailboxWarmupAnalyticsStub struct {
	analytics.AnalyticsService
	statuses []models.EmailAccountStatus
}

func (s mailboxWarmupAnalyticsStub) GetAllAccountStatuses(context.Context, uuid.UUID) ([]models.EmailAccountStatus, *errx.Error) {
	return s.statuses, nil
}

type mailboxWarmupEmailStub struct {
	email.EmailService
	mailbox *models.Email
}

func (s mailboxWarmupEmailStub) Get(context.Context, string, string) (*models.Email, *errx.Error) {
	return s.mailbox, nil
}

// An agent reading a Cloud-warmed mailbox must be told it is warming, though the local warmup column is NULL.
func TestMailboxToolsReportCloudWarmup(t *testing.T) {
	id := uuid.New()
	inv := Invocation{OrgID: uuid.New(), UserID: uuid.New(), OrgPerms: models.PermManageEmails | models.PermViewCampaigns | models.PermViewAnalytics}

	listed := models.EmailAccountStatus{ID: id, Email: "a@example.com", WarmupStatus: &models.WarmupStatusInfo{Source: models.WarmupSourceCloud, State: models.WarmupStateActive, Enabled: true}}
	registry := NewRegistry()
	d := Deps{Analytics: mailboxWarmupAnalyticsStub{statuses: []models.EmailAccountStatus{listed}}}
	d.registerAnalyticsTools(registry)
	out, err := registry.Call(context.Background(), inv, "list_mailboxes", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Mailboxes []struct {
			Warmup *struct {
				Source, State string
				Enabled       bool
			} `json:"warmup"`
		} `json:"mailboxes"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Mailboxes) != 1 || list.Mailboxes[0].Warmup == nil || list.Mailboxes[0].Warmup.Source != "cloud" || list.Mailboxes[0].Warmup.State != "active" {
		t.Fatalf("list_mailboxes hid Cloud warmup: %s", out)
	}

	active := models.WarmupStateActive
	mailbox := &models.Email{ID: id, CloudWarmup: &models.CloudLinkWarmup{EnrollmentState: "active", State: &active}}
	w := mailbox.EffectiveWarmup()
	mailbox.WarmupEffective = &w
	registry = NewRegistry()
	Deps{Emails: mailboxWarmupEmailStub{mailbox: mailbox}}.registerMailboxTools(registry)
	out, err = registry.Call(context.Background(), inv, "get_mailbox", json.RawMessage(`{"email_account_id":"`+id.String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Warmup          *string `json:"warmup"`
		WarmupEffective *struct {
			Source, State string
		} `json:"warmup_effective"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.WarmupEffective == nil || got.WarmupEffective.Source != "cloud" || got.WarmupEffective.State != "active" {
		t.Fatalf("get_mailbox hid Cloud warmup: %s", out)
	}
}
