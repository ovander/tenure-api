//go:build integration

package itest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/jobs"
)

// TestJobsAtStart: a job whose AtStart asks for it runs when the scheduler
// starts, and irl.fetch asks only while no IRL is cached (#258).
func TestJobsAtStart(t *testing.T) {
	var ran atomic.Int32
	r := &jobs.Runner{DB: env.jobsDB, Now: time.Now, Jobs: map[string]jobs.Job{
		"itest.at_start": {Name: "itest.at_start", Period: jobs.HourKey,
			Platform: func(context.Context, jobs.Owner) (any, error) { ran.Add(1); return nil, nil },
			AtStart:  func(context.Context) (bool, error) { return true, nil }},
		"itest.not_at_start": {Name: "itest.not_at_start", Period: jobs.HourKey,
			Platform: func(context.Context, jobs.Owner) (any, error) { t.Error("ran without being asked"); return nil, nil },
			AtStart:  func(context.Context) (bool, error) { return false, nil }},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	for i := 0; i < 100 && ran.Load() == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 1 {
		t.Errorf("at start: ran %d times", ran.Load())
	}

	irl := env.app.Runner.Jobs["irl.fetch"]
	if irl.AtStart == nil {
		t.Fatal("irl.fetch has no AtStart")
	}
	if n := superCount(t, "SELECT count(*) FROM irl_index"); n > 0 {
		if need, err := irl.AtStart(context.Background()); err != nil || need {
			t.Errorf("irl.fetch at start with %d quarters cached: %v %v", n, need, err)
		}
	}
	// With none cached it asks to run; the rows are put back.
	superExec(t, "CREATE TABLE itest_irl_keep AS SELECT * FROM irl_index")
	superExec(t, "DELETE FROM irl_index")
	t.Cleanup(func() {
		superExec(t, "INSERT INTO irl_index SELECT * FROM itest_irl_keep")
		superExec(t, "DROP TABLE itest_irl_keep")
	})
	if need, err := irl.AtStart(context.Background()); err != nil || !need {
		t.Errorf("irl.fetch at start with no IRL cached: %v %v", need, err)
	}
}
