package jobs

import (
	"context"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/models"
)

func (s *JobsService) markRiskBandFromWarmupHealth(ctx context.Context, accountID uuid.UUID, health *models.WarmupParticipantHealth) {
	if s.WorkerRepo == nil {
		return
	}

	state := models.WarmupHealthHealthy
	if health != nil {
		state = health.HealthState
	} else if resolved, ok := s.resolveWarmupHealthState(ctx, accountID); ok {
		state = resolved
	} else {
		// No reading is no verdict: an error or a missing Cloud report keeps the band.
		return
	}

	band := models.RiskBandFromHealth(state)
	if err := s.WorkerRepo.SetEmailAccountRiskBand(ctx, accountID, band); err != nil {
		log.Warn().
			Err(err).
			Str("account_id", accountID.String()).
			Str("risk_band", string(band)).
			Msg("failed to update account risk band from warmup health")
	}
}

func (s *JobsService) resolveWarmupHealthState(ctx context.Context, accountID uuid.UUID) (models.WarmupHealthState, bool) {
	if s.WarmupRepo == nil {
		return "", false
	}

	// The mailbox's standing, including one Warmbly Cloud reported for a
	// mailbox it warms, so a local event cannot reset a cloud hold's band.
	state, cloudUnknown, err := s.WarmupRepo.GetKnownHealthState(ctx, accountID)
	if err != nil || (cloudUnknown && !state.Holds()) {
		return "", false
	}
	return state, true
}
