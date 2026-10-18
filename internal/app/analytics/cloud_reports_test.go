package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type cloudWarmupStub struct {
	stats     []models.WarmupDailyStats
	placement *models.WarmupPlacementData
	failure   *errx.Error
	calls     int
}

func (s *cloudWarmupStub) WarmupStats(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time) ([]models.WarmupDailyStats, *errx.Error) {
	s.calls++
	return s.stats, s.failure
}
func (s *cloudWarmupStub) WarmupPlacementData(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time) (*models.WarmupPlacementData, *errx.Error) {
	s.calls++
	return s.placement, s.failure
}

type reportPlacementRepo struct {
	repository.WarmupPlacementRepository
	data models.WarmupPlacementData
}

func (r reportPlacementRepo) Daily(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time) ([]repository.WarmupPlacementDayRow, error) {
	return r.data.Daily, nil
}
func (r reportPlacementRepo) Hosts(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time) ([]repository.WarmupPlacementHostRow, error) {
	return r.data.Hosts, nil
}
func (r reportPlacementRepo) Sent(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time) ([]repository.WarmupSenderDayCount, error) {
	return r.data.Sent, nil
}
func (r reportPlacementRepo) Unconfirmed(context.Context, uuid.UUID, *uuid.UUID, time.Time, time.Time, time.Time) ([]repository.WarmupSenderDayCount, error) {
	return r.data.Unconfirmed, nil
}
func (r reportPlacementRepo) Rates(context.Context, uuid.UUID, *uuid.UUID, time.Time) (map[uuid.UUID]models.WarmupPlacementWindow, error) {
	return r.data.Windows, nil
}

func TestCloudAndLocalWarmupHistoryMergeByDay(t *testing.T) {
	cloud := &cloudWarmupStub{stats: []models.WarmupDailyStats{{Date: "2026-10-01", EmailsSent: 9, EmailsReplied: 3, EmailsReceived: 4, TargetVolume: 10, Active: true}}}
	s := &analyticsService{analyticsRepo: warmupAnalyticsRepoStub{stats: []models.WarmupDailyStats{
		{Date: "2026-09-30", EmailsSent: 2, TargetVolume: 2, Active: true},
		{Date: "2026-10-01", EmailsSent: 1, EmailsReceived: 2, TargetVolume: 2, Active: true},
	}}, cloudReports: cloud}
	report, xerr := s.GetWarmupAnalytics(context.Background(), uuid.New(), nil, time.Time{}, time.Now())
	if xerr != nil || len(report.DailyStats) != 2 || report.Summary.TotalSent != 12 || report.Summary.TotalReplied != 3 || report.Summary.TotalReceived != 6 || report.Summary.DaysActive != 2 || report.Summary.AverageDaily != 6 || report.Summary.ReplyRate != 25 {
		t.Fatalf("report=%+v error=%v", report, xerr)
	}
	cloud.failure = errx.ErrServiceDown
	if report, xerr := s.GetWarmupAnalytics(context.Background(), uuid.New(), nil, time.Time{}, time.Now()); xerr == nil || report != nil {
		t.Fatalf("returned a partial report during Cloud failure: %+v, %v", report, xerr)
	}
}

func TestMixedPlacementUsesCountsAndCloudLookbackNotAveragedRates(t *testing.T) {
	localID, cloudID := uuid.New(), uuid.New()
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	local := reportPlacementRepo{data: models.WarmupPlacementData{
		Daily:   []models.WarmupPlacementDayRow{{SenderID: localID, Email: "local@test.local", Date: "2026-10-01", Group: "google", Spam: 2}},
		Hosts:   []models.WarmupPlacementHostRow{{Group: "google", Host: "google_workspace", Spam: 2}},
		Sent:    []models.WarmupSenderDayCount{{SenderID: localID, Email: "local@test.local", Date: "2026-10-01", Count: 2}},
		Windows: map[uuid.UUID]models.WarmupPlacementWindow{localID: {Major: models.WarmupPlacementTally{Spam: 10}, All: models.WarmupPlacementTally{Spam: 10}}},
	}}
	cloud := &cloudWarmupStub{placement: &models.WarmupPlacementData{
		Daily: []models.WarmupPlacementDayRow{
			{SenderID: cloudID, Email: "cloud@test.local", Date: "2026-09-30", Group: "google", Inbox: 18, Spam: 2},
			{SenderID: cloudID, Email: "cloud@test.local", Date: "2026-10-01", Group: "google", Inbox: 8, Spam: 2},
		},
		Hosts:   []models.WarmupPlacementHostRow{{Group: "google", Host: "google_workspace", Inbox: 8, Spam: 2}},
		Sent:    []models.WarmupSenderDayCount{{SenderID: cloudID, Email: "cloud@test.local", Date: "2026-10-01", Count: 10}},
		Windows: map[uuid.UUID]models.WarmupPlacementWindow{cloudID: {Major: models.WarmupPlacementTally{Inbox: 90, Spam: 10}, All: models.WarmupPlacementTally{Inbox: 90, Spam: 10}}},
	}}
	s := &analyticsService{placementRepo: local, cloudReports: cloud}
	report, xerr := s.GetWarmupPlacement(context.Background(), uuid.New(), nil, day, day)
	if xerr != nil || report.Summary.Delivered != 12 || report.Summary.Sent != 12 || report.Rate.InboxRate == nil || *report.Rate.InboxRate != 81.82 || len(report.Mailboxes) != 2 {
		t.Fatalf("report=%+v error=%v", report, xerr)
	}
	if len(report.Providers) != 1 || len(report.Providers[0].Hosts) != 1 || report.Providers[0].Hosts[0].Delivered != 12 || report.Daily[0].RollingInboxRate == nil || *report.Daily[0].RollingInboxRate != 81.25 {
		t.Fatalf("hosts or rolling lookback were not merged: %+v, %+v", report.Providers, report.Daily)
	}
	cloud.failure = errx.ErrServiceDown
	if report, xerr := s.GetWarmupPlacement(context.Background(), uuid.New(), nil, day, day); xerr == nil || report != nil {
		t.Fatalf("partial placement: %+v, %v", report, xerr)
	}
}

type foreignReportEmailRepo struct{ repository.EmailRepository }

func (foreignReportEmailRepo) Get(context.Context, string, string) (*models.Email, *errx.Error) {
	return nil, errx.ErrNotFound
}

func TestCloudReportsRefuseMailboxesOutsideTheLocalWorkspace(t *testing.T) {
	cloud := &cloudWarmupStub{}
	s := &analyticsService{analyticsRepo: warmupAnalyticsRepoStub{}, emailRepo: foreignReportEmailRepo{}, cloudReports: cloud}
	id, org := uuid.New(), uuid.New()
	day := time.Now().UTC()
	if _, xerr := s.GetWarmupAnalytics(context.Background(), org, &id, day, day); xerr != errx.ErrNotFound {
		t.Fatalf("stats ownership: %v", xerr)
	}
	if _, xerr := s.GetWarmupPlacement(context.Background(), org, &id, day, day); xerr != errx.ErrNotFound {
		t.Fatalf("placement ownership: %v", xerr)
	}
	if cloud.calls != 0 {
		t.Fatal("foreign mailbox was requested from Cloud")
	}
}

func TestListPlacementRatesIncludeCloudWarmedMailboxes(t *testing.T) {
	localID, cloudID := uuid.New(), uuid.New()
	local := reportPlacementRepo{data: models.WarmupPlacementData{
		Windows: map[uuid.UUID]models.WarmupPlacementWindow{localID: {Major: models.WarmupPlacementTally{Inbox: 20}, All: models.WarmupPlacementTally{Inbox: 20}}},
	}}
	cloud := &cloudWarmupStub{placement: &models.WarmupPlacementData{
		Windows: map[uuid.UUID]models.WarmupPlacementWindow{cloudID: {Major: models.WarmupPlacementTally{Inbox: 3, Spam: 2}, All: models.WarmupPlacementTally{Inbox: 5, Spam: 2}}},
	}}
	s := &analyticsService{placementRepo: local, cloudReports: cloud}
	rates := s.placementRates(context.Background(), uuid.New(), nil)
	if r, ok := rates[cloudID]; !ok || r.Delivered != 5 || r.InboxRate != nil {
		t.Fatalf("cloud-warmed mailbox missing from list rates: %+v", rates)
	}
	if r, ok := rates[localID]; !ok || r.InboxRate == nil {
		t.Fatalf("local rate lost: %+v", rates)
	}
	cloud.failure = errx.ErrServiceDown
	rates = s.placementRates(context.Background(), uuid.New(), nil)
	if _, ok := rates[localID]; !ok || len(rates) != 1 {
		t.Fatalf("a Cloud failure must leave local rates intact: %+v", rates)
	}
}
