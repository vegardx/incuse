package orchestrator

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/netwerk-io/incuse/internal/incus"
)

// reapOnce is one sweep. Three responsibilities:
//
//  1. Registration timeout: a launching/idle runner that's been
//     around longer than runner.registration_timeout is presumed
//     dead (cloud-init broken, image broken, network broken).
//
//  2. Max-job-duration: a busy runner that's been busy longer than
//     runner.max_job_duration. Defence-in-depth — JobCompleted
//     normally tears it down before this fires.
//
//  3. Drift sweep: any instance managed by this scale set that the
//     local tracker has no record of is an orphan from a previous
//     orchestrator process. Recover by deleting it. Covers crash
//     recovery without touching another class's runners.
func (o *Orchestrator) reapOnce(ctx context.Context) {
	now := o.cfg.Now()
	tracked := o.tracker.snapshot()
	o.parallelReap(ctx, len(tracked), func(i int) {
		o.reapTracked(ctx, tracked[i], now)
	})
	if ctx.Err() != nil {
		return
	}
	o.driftSweep(ctx)
}

// reapTracked applies the timeout rules for one in-tracker runner.
func (o *Orchestrator) reapTracked(ctx context.Context, r trackedRunner, now time.Time) {
	switch r.State {
	case statusBusy:
		if now.Sub(r.BusyAt) > o.cfg.RunnerCfg.MaxJobDuration {
			o.cfg.Logger.Warn("reaping busy runner past max_job_duration",
				"runner_name", r.Name,
				"running_for", now.Sub(r.BusyAt),
			)
			o.cfg.Metrics.Reap("max_job_duration")
			o.terminateInstance(ctx, r.Name, "max job duration")
			o.removeRunnerByID(ctx, r.RunnerID, r.Name, "max job duration")
		}
	default: // statusLaunching, statusIdle
		if now.Sub(r.MintedAt) > o.cfg.RunnerCfg.RegistrationTimeout {
			o.cfg.Logger.Warn("reaping runner that never picked up a job",
				"runner_name", r.Name,
				"state", r.State,
				"age", now.Sub(r.MintedAt),
			)
			o.cfg.Metrics.Reap("registration_timeout")
			o.terminateInstance(ctx, r.Name, "registration timeout")
			o.removeRunnerByID(ctx, r.RunnerID, r.Name, "registration timeout")
		}
	}
}

// driftSweep enumerates every instance in the incuse project,
// filters to ones tagged user.incuse.managed=true, and deletes any
// whose name is not in the local tracker.
func (o *Orchestrator) driftSweep(ctx context.Context) {
	remote, err := o.cfg.IncusClient.List(ctx, o.cfg.IncusCfg.Project)
	if err != nil {
		o.cfg.Logger.Warn("drift sweep list failed", "error", err)
		return
	}
	scaleSetID := strconv.Itoa(o.cfg.ScaleSet.ScaleSetID())
	known := o.tracker.names()
	var orphans []incus.Instance
	for _, inst := range remote {
		if inst.Config[metaManaged] != "true" ||
			inst.Config[metaScaleSetID] != scaleSetID {
			continue
		}
		if _, tracked := known[inst.Name]; tracked {
			continue
		}
		orphans = append(orphans, inst)
	}
	o.parallelReap(ctx, len(orphans), func(i int) {
		inst := orphans[i]
		o.cfg.Logger.Warn("reaping orphan instance",
			"runner_name", inst.Name,
			"status", inst.Status,
		)
		o.cfg.Metrics.Reap("drift_sweep")
		if err := o.cfg.IncusClient.Stop(ctx, inst.Name); err != nil {
			o.cfg.Logger.Warn("orphan stop failed", "runner_name", inst.Name, "error", err)
		}
		if err := o.cfg.IncusClient.Delete(ctx, inst.Name); err != nil {
			o.cfg.Logger.Warn("orphan delete failed", "runner_name", inst.Name, "error", err)
		}
	})
}

func (o *Orchestrator) parallelReap(
	ctx context.Context,
	count int,
	reap func(i int),
) {
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		if !o.launchLimiter.acquire(ctx) {
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer o.launchLimiter.release()
			reap(i)
		}()
	}
	wg.Wait()
}
