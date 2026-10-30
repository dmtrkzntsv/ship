package host

import (
	"context"
	"fmt"
	"time"
)

// journalLines is how many log lines a failed health check prints.
const journalLines = 30

// WaitHealthy checks that unit stays active for HealthWait, polling every
// HealthTick. It fails as soon as the unit is seen inactive; the error
// carries the last journal lines of the unit.
func (h *Host) WaitHealthy(ctx context.Context, unit string) error {
	deadline := time.Now().Add(h.HealthWait)
	tick := h.HealthTick
	if tick <= 0 {
		tick = time.Second
	}
	for {
		if !h.Sys.IsActive(ctx, unit) {
			return fmt.Errorf("%s failed to come up%s", unit, h.logTail(ctx, unit))
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(tick, time.Until(deadline))):
		}
	}
}

// logTail is the "last log lines" block appended to failure errors.
func (h *Host) logTail(ctx context.Context, unit string) string {
	return "\n--- last log lines ---\n" + h.Sys.JournalTail(ctx, unit, journalLines)
}
