package repository

import (
	"testing"
	"time"

	"github.com/warmbly/warmbly/internal/models"
)

// Cloud's own report stands only when it is the live hold the gate enforces;
// any other live hold is shown, with its own end.
func TestCloudStatusShowsTheHoldTheGateEnforces(t *testing.T) {
	now := time.Now()
	later, earlier, past := now.Add(48*time.Hour), now.Add(time.Hour), now.Add(-time.Hour)
	quarantined := string(models.WarmupHealthQuarantined)
	reason := "complaints"
	known := cloudStatusRow{state: &quarantined, until: &later, reason: &reason}
	for _, tc := range []struct {
		name    string
		report  *models.WarmupHealthInfo
		unknown bool
		want    *time.Time
		cloud   bool
	}{
		{"same live row", &models.WarmupHealthInfo{State: quarantined, BlockedUntil: &later, PoolType: "premium"}, false, &later, true},
		{"earlier end", &models.WarmupHealthInfo{State: quarantined, BlockedUntil: &earlier}, false, &later, false},
		{"lapsed end", &models.WarmupHealthInfo{State: quarantined, BlockedUntil: &past}, false, &later, false},
		{"milder report", &models.WarmupHealthInfo{State: "healthy"}, false, &later, false},
		{"unknown evidence", &models.WarmupHealthInfo{State: quarantined, BlockedUntil: &later}, true, &later, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := known
			c.unknown = tc.unknown
			m := &models.CloudLinkMailbox{Standing: tc.report}
			got := c.status(m, now)
			if (got == tc.report) != tc.cloud || got.State != quarantined || got.BlockedUntil == nil || !got.BlockedUntil.Equal(*tc.want) {
				t.Fatalf("status = %+v, want quarantined until %v (Cloud's report: %v)", got, tc.want, tc.cloud)
			}
		})
	}
	if got := (cloudStatusRow{unknown: true}).status(&models.CloudLinkMailbox{}, now); !got.CloudEvidenceUnavailable() {
		t.Fatalf("unknown with no hold = %+v, want unavailable", got)
	}
}
