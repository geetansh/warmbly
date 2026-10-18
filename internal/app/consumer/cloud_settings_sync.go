package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/jobrun"
)

// CloudSettingsSyncer is the part of the cloud link that mirrors the warmup settings Warmbly Cloud owns.
type CloudSettingsSyncer interface {
	SyncWarmupSettings(ctx context.Context) ([]uuid.UUID, *errx.Error)
}

// StartCloudSettingsSync keeps each Cloud-warmed mailbox's local settings equal to the cloud's, so edits made there show here.
func (s *JobsService) StartCloudSettingsSync(ctx context.Context, interval time.Duration) {
	syncer, ok := s.CloudLink.(CloudSettingsSyncer)
	if !ok || syncer == nil {
		return
	}
	jobrun.Loop(ctx, "cloud_settings_sync", interval, true, func(ctx context.Context) error {
		return s.syncCloudSettings(ctx, syncer)
	})
}

func (s *JobsService) syncCloudSettings(ctx context.Context, syncer CloudSettingsSyncer) error {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	changed, xerr := syncer.SyncWarmupSettings(runCtx)
	for _, id := range changed {
		s.publishAccountSynced(runCtx, id)
	}
	if len(changed) > 0 {
		log.Info().Int("mailboxes", len(changed)).Msg("cloud settings sync: warmup settings changed in Warmbly Cloud")
	}
	if xerr != nil {
		return xerr
	}
	return nil
}

func (s *JobsService) publishAccountSynced(ctx context.Context, accountID uuid.UUID) {
	if s.StreamingPublisher == nil || s.EmailRepository == nil {
		return
	}
	acc, xerr := s.EmailRepository.GetByID(ctx, accountID)
	if xerr != nil || acc == nil {
		return
	}
	orgID := ""
	if acc.OrganizationID != nil {
		orgID = acc.OrganizationID.String()
	}
	s.StreamingPublisher.PublishAccountSynced(ctx, orgID, acc.UserID, acc.ID.String(), acc.Email, acc.Provider, acc.Status)
}
