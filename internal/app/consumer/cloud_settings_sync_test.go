package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
)

type settingsSyncer struct {
	changed []uuid.UUID
	err     *errx.Error
	calls   int
}

func (s *settingsSyncer) SyncWarmupSettings(context.Context) ([]uuid.UUID, *errx.Error) {
	s.calls++
	return s.changed, s.err
}

func TestCloudSettingsSyncReportsAFailedRefresh(t *testing.T) {
	s := &JobsService{}
	syncer := &settingsSyncer{changed: []uuid.UUID{uuid.New()}, err: errx.NewWithIdentifier(errx.ServiceUnavailable, "cloud_link_unreachable", "unreachable")}
	if err := s.syncCloudSettings(context.Background(), syncer); err == nil || syncer.calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, syncer.calls)
	}
	syncer.err = nil
	if err := s.syncCloudSettings(context.Background(), syncer); err != nil {
		t.Fatal(err)
	}
}

// The job only runs where the cloud link can mirror settings.
func TestCloudSettingsSyncIsANoOpWithoutALink(t *testing.T) {
	s := &JobsService{CloudLink: &warmupInboxCloud{enrollmentErr: errors.New("unused")}}
	s.StartCloudSettingsSync(context.Background(), 0)
}
