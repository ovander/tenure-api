package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/ovander/tenure-api/internal/audit"
)

// ErrChainMismatch reports a chain whose recomputed hashes differ from the
// stored ones.
var ErrChainMismatch = errors.New("audit chain mismatch")

func verifyChain(ctx context.Context) (any, error) {
	res, err := audit.Verify(ctx)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return res, fmt.Errorf("%w at event %d: %s", ErrChainMismatch, res.BadID, res.Reason)
	}
	return res, nil
}

// AuditVerify recomputes every owner's chain and the platform chain weekly
// (RENT-BE-001 §5, §8b); a mismatch fails that owner and the run ends partial
// or failed, which alerts. Comparing with the daily heads in the locked bucket
// arrives with audit.head in slice 3.
func AuditVerify() Job {
	return Job{
		Name:     "audit.verify",
		Schedule: "0 5 * * 0",
		Period:   WeekKey,
		PerOwner: func(ctx context.Context, _ Owner) (any, error) { return verifyChain(ctx) },
		Platform: func(ctx context.Context, _ bool) (any, error) { return verifyChain(ctx) },
	}
}

// Registry returns the jobs of slice 2.
func Registry() map[string]Job {
	j := AuditVerify()
	return map[string]Job{j.Name: j}
}
