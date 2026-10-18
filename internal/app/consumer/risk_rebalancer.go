package jobs

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/jobrun"
	"github.com/warmbly/warmbly/internal/models"
)

// StartRiskRebalancer recomputes each mailbox's risk band from its warmup
// health state.
//
// It used to also migrate mailboxes so a "risky" one never shared a worker
// with a clean one. That segregation protected nothing: the worker is not the
// sending identity, so a mailbox landing in spam cannot drag down a neighbour
// whose mail leaves through an entirely different provider. The band survives
// because warmup partner selection and per-mailbox pacing genuinely use it.
//
// Why a periodic batch instead of event-driven: warmup health state changes on
// a slow rolling-window basis (the warmup_health_sweep job runs hourly), so
// reacting in real time doesn't buy much.
func (s *JobsService) StartRiskRebalancer(ctx context.Context, interval time.Duration) {
	if s.WorkerRepo == nil {
		return
	}
	// The boot pass (so a fresh deploy converges quickly) keeps its shorter budget.
	first := true
	jobrun.Loop(ctx, "risk_rebalancer", interval, true, func(ctx context.Context) error {
		timeout := 10 * time.Minute
		if first {
			first = false
			timeout = 5 * time.Minute
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		s.rebalanceRisk(runCtx)
		return nil
	})
}

func (s *JobsService) rebalanceRisk(ctx context.Context) {
	candidates, err := s.WorkerRepo.ListRiskCandidates(ctx, 1000)
	if err != nil {
		log.Warn().Err(err).Msg("risk band sweep: list candidates failed")
		return
	}

	var updatedBand int

	for _, c := range candidates {
		// A missing Cloud report is no verdict; only a real hold beside it moves the band.
		if c.CloudEvidenceUnknown && !c.HealthState.Holds() {
			continue
		}
		newBand := models.RiskBandFromHealth(c.HealthState)
		if newBand == c.CurrentBand {
			continue
		}
		if err := s.WorkerRepo.SetEmailAccountRiskBand(ctx, c.EmailAccountID, newBand); err != nil {
			log.Warn().Err(err).Str("account_id", c.EmailAccountID.String()).Msg("risk band sweep: set band failed")
			continue
		}
		updatedBand++
	}

	if updatedBand > 0 {
		log.Info().
			Int("updated_bands", updatedBand).
			Int("scanned", len(candidates)).
			Msg("risk band sweep complete")
	}
}
