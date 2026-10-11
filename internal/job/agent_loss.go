package job

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kongkx/cloud-print-service/internal/domain"
	"github.com/kongkx/cloud-print-service/internal/observ"
	"github.com/kongkx/cloud-print-service/internal/store"
)

// FailOnAgentLost fails the queued jobs of every printer the departed agent owned.
//
// Design: a queue that keeps backing up to a dead agent is invisible work — it looks like
// "jobs are queued" when in fact they will never print until the agent returns. Failing
// them loudly with a clear reason lets the caller decide to resubmit elsewhere, which is
// the correct response to a dead packing PC.
func (s *Service) FailOnAgentLost(ctx context.Context, printerIDs []string, reason string) int {
	if len(printerIDs) == 0 {
		return 0
	}
	if reason == "" {
		reason = "agent lost"
	}
	reason = strings.TrimSpace(reason)
	failed := 0
	jobs, err := s.List(ctx, store.Filter{State: string(domain.JobQueued), Limit: 1000})
	if err != nil {
		s.log.Error("cannot list queued jobs for agent loss", "err", err)
		return 0
	}
	owned := make(map[string]bool, len(printerIDs))
	for _, id := range printerIDs {
		owned[id] = true
	}
	for i := range jobs {
		j := &jobs[i]
		if !owned[j.PrinterID] {
			continue
		}
		s.fail(ctx, j, fmt.Sprintf("%s: printer %s is unreachable", reason, j.PrinterID))
		s.met.Inc("cps_jobs_failed_agent_loss_total", "printer_id", j.PrinterID)
		failed++
		s.bus.Publish(observ.Event{Topic: "job.state", Data: map[string]any{
			"job_id": j.ID, "printer_id": j.PrinterID, "state": string(domain.JobFailed),
			"at": time.Now().UTC(),
		}})
	}
	if failed > 0 {
		s.log.Warn("queued jobs failed because the agent is gone", "jobs", failed, "reason", reason)
	}
	return failed
}

// FailOnAgentUnhealthy is the softer variant: it marks the jobs that can still be requeued
// as failed with a hint, without touching jobs that are currently printing.
func (s *Service) FailOnAgentUnhealthy(ctx context.Context, printerIDs []string) int {
	return s.FailOnAgentLost(ctx, printerIDs, "agent is not heart-beating")
}
