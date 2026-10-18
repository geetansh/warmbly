package analytics

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

func TestPlacementBuilderRollingReadsTheLookback(t *testing.T) {
	from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	sender := uuid.New()
	b := newPlacementBuilder(from, to)
	// Before the range: only the rolling rate may see it.
	b.addDelivery(repository.WarmupPlacementDayRow{SenderID: sender, Date: "2026-09-05", Group: "google", Inbox: 18, Spam: 2})
	b.addDelivery(repository.WarmupPlacementDayRow{SenderID: sender, Date: "2026-09-10", Group: "microsoft", Inbox: 4, Tabs: 1, Spam: 5, Rescued: 4})
	// A small host counts in the day, never in the rolling rate.
	b.addDelivery(repository.WarmupPlacementDayRow{SenderID: sender, Date: "2026-09-10", Group: "other", Spam: 10})

	days := b.days()
	if len(days) != 2 {
		t.Fatalf("got %d days", len(days))
	}
	d := days[0]
	if d.Delivered != 20 || d.Spam != 15 || d.Rescued != 4 || *d.InboxRate != 25 {
		t.Fatalf("day counts: %+v", d.WarmupPlacementCounts)
	}
	if d.RollingInboxRate == nil || *d.RollingInboxRate != 76.67 {
		t.Fatalf("rolling rate over 30 deliveries: %v", d.RollingInboxRate)
	}
	if len(d.Groups) != 2 || d.Groups[0].Group != "microsoft" {
		t.Fatalf("groups: %+v", d.Groups)
	}
	if days[1].Delivered != 0 || days[1].InboxRate != nil {
		t.Fatalf("an empty day has no rate: %+v", days[1])
	}

	boxes := b.mailboxes(map[uuid.UUID]string{sender: "a@example.com"}, nil)
	if len(boxes) != 1 || boxes[0].Delivered != 20 || boxes[0].Rate.Band != models.WarmupPlacementBandNone {
		t.Fatalf("mailboxes: %+v", boxes)
	}
}

func TestApplyWarmupPlacementCapsHealth(t *testing.T) {
	h := models.AccountHealth{Status: "healthy", Score: 100}
	applyWarmupPlacement(&h, nil)
	if h.Score != 100 {
		t.Fatalf("no reading leaves health alone: %+v", h)
	}
	// Small hosts alone leave no headline, so nothing is held against the mailbox.
	small := models.WarmupPlacementWindow{All: models.WarmupPlacementTally{Inbox: 50, Spam: 50}}.Rate()
	applyWarmupPlacement(&h, &small)
	if h.Score != 100 || h.Status != "healthy" {
		t.Fatalf("a small-host reading moved health: %+v", h)
	}
	major := func(inbox, spam int) models.WarmupPlacementRate {
		return models.WarmupPlacementWindow{Major: models.WarmupPlacementTally{Inbox: inbox, Spam: spam}}.Rate()
	}
	r := major(97, 3)
	applyWarmupPlacement(&h, &r)
	if h.Score != 97 || h.Status != "healthy" || len(h.Issues) != 0 {
		t.Fatalf("97%% caps the score and stays healthy: %+v", h)
	}
	r = major(70, 30)
	applyWarmupPlacement(&h, &r)
	if h.Score != 70 || h.Status != "warning" || len(h.Issues) != 1 {
		t.Fatalf("70%% warns: %+v", h)
	}
}

func TestWarmupHealthKeepsReducedVolumeAsWarningWithoutDoublePenalty(t *testing.T) {
	for _, tc := range []struct {
		name, state, initialStatus, wantStatus string
		initialScore, wantScore                int
	}{
		{"watch", "watch", "healthy", "warning", 100, 93},
		{"throttle", "throttled", "healthy", "warning", 100, 93},
		{"connection error", "throttled", "error", "error", 60, 60},
		{"existing warning", "watch", "warning", "warning", 80, 80},
		{"quarantine", "quarantined", "healthy", "error", 100, 50},
		{"block", "blocked", "healthy", "error", 100, 30},
		{"score floor", "blocked", "error", "error", 20, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := models.AccountHealth{Status: tc.initialStatus, Score: tc.initialScore}
			r := models.WarmupPlacementWindow{Major: models.WarmupPlacementTally{Inbox: 28, Spam: 2}}.Rate()
			applyWarmupHealth(&h, &models.WarmupHealthInfo{State: tc.state, Reason: "current health evidence"})
			applyWarmupPlacement(&h, &r)
			if h.Score != tc.wantScore || h.Status != tc.wantStatus || len(h.Issues) != 1 {
				t.Fatalf("health = %+v, want %s %d and one reason", h, tc.wantStatus, tc.wantScore)
			}
		})
	}
}

// A missing Warmbly Cloud report holds sends but is no verdict on reputation:
// a warning with the reason, never an error or a lower score.
func TestWarmupHealthTreatsUnknownCloudEvidenceAsAWarning(t *testing.T) {
	for _, tc := range []struct {
		name, initialStatus, wantStatus string
	}{
		{"healthy", "healthy", "warning"},
		{"existing error", "error", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := models.AccountHealth{Status: tc.initialStatus, Score: 90}
			applyWarmupHealth(&h, models.CloudStandingUnavailable())
			if h.Score != 90 || h.Status != tc.wantStatus || len(h.Issues) != 1 || h.Issues[0] != "Warmbly Cloud status unavailable" {
				t.Fatalf("health = %+v, want %s at 90 with the unavailable reason", h, tc.wantStatus)
			}
		})
	}
	if got := warmupHealthState(models.CloudStandingUnavailable()); got != "" {
		t.Fatalf("ramp band = %q, want none", got)
	}
}
