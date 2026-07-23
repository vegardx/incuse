// Package observability owns the metrics + health surfaces for
// incuse. The package exposes one type, Recorder, that:
//
//   - implements sslistener.MetricsRecorder so the upstream listener
//     populates GitHub-side gauges/counters automatically;
//   - exposes incuse-specific Record* methods the orchestrator calls
//     from mint, launch, termination, and reaper paths;
//   - holds a *prometheus.Registry so the http server can scrape it
//     without touching globals.
//
// All metric names live under the `incuse_` prefix. Labels are kept
// minimal to avoid runaway cardinality on a host that may launch
// thousands of VMs over its lifetime.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"

	ssapi "github.com/actions/scaleset"
)

// Recorder bundles every collector incuse emits. Build via New, and
// pass to scaleset.Options.MetricsRecorder + orchestrator.Config.Recorder.
type Recorder struct {
	registry *prometheus.Registry

	// Job lifecycle counters.
	runnersSpawned *prometheus.CounterVec
	jobsStarted    *prometheus.CounterVec
	jobsCompleted  *prometheus.CounterVec

	// Launch + reap.
	launches *prometheus.CounterVec
	reaps    *prometheus.CounterVec

	// Latency histograms.
	launchDuration *prometheus.HistogramVec
	runnerLifetime *prometheus.HistogramVec

	// Live state.
	trackedInstances *prometheus.GaugeVec
	desiredRunners   *prometheus.GaugeVec

	// GitHub-side state mirrored from RunnerScaleSetStatistic.
	statTotalAvailableJobs     *prometheus.GaugeVec
	statTotalAcquiredJobs      *prometheus.GaugeVec
	statTotalAssignedJobs      *prometheus.GaugeVec
	statTotalRunningJobs       *prometheus.GaugeVec
	statTotalRegisteredRunners *prometheus.GaugeVec
	statTotalBusyRunners       *prometheus.GaugeVec
	statTotalIdleRunners       *prometheus.GaugeVec

	// Build-info gauge — convenient for "what version is running".
	buildInfo *prometheus.GaugeVec
}

// New wires every collector to a fresh registry and returns a ready
// Recorder. Callers may pass Recorder.Registry() to the http server.
func New(version, commit string) *Recorder {
	r := prometheus.NewRegistry()
	rec := &Recorder{
		registry: r,
		runnersSpawned: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "incuse",
			Name:      "runners_spawned_total",
			Help:      "Number of idle runners incuse has spawned to satisfy GitHub's desired-runner-count.",
		}, []string{"scale_set"}),
		jobsStarted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "incuse",
			Name:      "jobs_started_total",
			Help:      "Number of GitHub JobStarted messages observed.",
		}, []string{"scale_set"}),
		jobsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "incuse",
			Name:      "jobs_completed_total",
			Help:      "Number of GitHub JobCompleted messages observed.",
		}, []string{"scale_set", "result"}),
		launches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "incuse",
			Name:      "launches_total",
			Help:      "Incus VM launch attempts.",
		}, []string{"scale_set", "result"}),
		reaps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "incuse",
			Name:      "reaps_total",
			Help:      "Reaper terminations bucketed by reason.",
		}, []string{"scale_set", "reason"}),
		launchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "incuse",
			Name:      "launch_duration_seconds",
			Help:      "Time spent inside IncusClient.Launch (create+start operation).",
			// VM cold-boot on commodity hardware lands in 5-30s; widen
			// past that to catch image pulls and slow daemons.
			Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300},
		}, []string{"scale_set"}),
		runnerLifetime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "incuse",
			Name:      "runner_lifetime_seconds",
			Help:      "End-to-end runner lifetime: JIT mint to termination.",
			// Job durations are bimodal: <2 min for unit tests, hours
			// for builds. Buckets cover both.
			Buckets: []float64{30, 60, 120, 300, 600, 1800, 3600, 7200, 21600},
		}, []string{"scale_set"}),
		trackedInstances: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "incuse",
			Name:      "tracked_instances",
			Help:      "Instances currently in the orchestrator's in-memory tracker.",
		}, []string{"scale_set"}),
		desiredRunners: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "incuse",
			Name:      "desired_runners",
			Help:      "Most recent desired-runner-count GitHub asked for.",
		}, []string{"scale_set"}),
		statTotalAvailableJobs:     newStatGauge("total_available_jobs"),
		statTotalAcquiredJobs:      newStatGauge("total_acquired_jobs"),
		statTotalAssignedJobs:      newStatGauge("total_assigned_jobs"),
		statTotalRunningJobs:       newStatGauge("total_running_jobs"),
		statTotalRegisteredRunners: newStatGauge("total_registered_runners"),
		statTotalBusyRunners:       newStatGauge("total_busy_runners"),
		statTotalIdleRunners:       newStatGauge("total_idle_runners"),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "incuse",
			Name:      "build_info",
			Help:      "Always 1; labels carry version + commit.",
		}, []string{"version", "commit"}),
	}
	rec.buildInfo.WithLabelValues(version, commit).Set(1)

	r.MustRegister(
		rec.runnersSpawned,
		rec.jobsStarted,
		rec.jobsCompleted,
		rec.launches,
		rec.reaps,
		rec.launchDuration,
		rec.runnerLifetime,
		rec.trackedInstances,
		rec.desiredRunners,
		rec.statTotalAvailableJobs,
		rec.statTotalAcquiredJobs,
		rec.statTotalAssignedJobs,
		rec.statTotalRunningJobs,
		rec.statTotalRegisteredRunners,
		rec.statTotalBusyRunners,
		rec.statTotalIdleRunners,
		rec.buildInfo,
	)
	return rec
}

func newStatGauge(name string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "incuse",
		Subsystem: "scaleset",
		Name:      name,
		Help:      "Mirrors the corresponding RunnerScaleSetStatistic field from the upstream API.",
	}, []string{"scale_set"})
}

// Registry returns the underlying registry so the http server can hand
// it to promhttp.HandlerFor.
func (r *Recorder) Registry() *prometheus.Registry {
	return r.registry
}

// ScaleSetRecorder binds every observation to one bounded class label.
type ScaleSetRecorder struct {
	recorder *Recorder
	name     string
}

// ForScaleSet returns the listener and orchestrator metrics hooks for
// one homogeneous runner class.
func (r *Recorder) ForScaleSet(name string) *ScaleSetRecorder {
	r.trackedInstances.WithLabelValues(name)
	r.desiredRunners.WithLabelValues(name)
	r.statTotalAvailableJobs.WithLabelValues(name)
	r.statTotalAcquiredJobs.WithLabelValues(name)
	r.statTotalAssignedJobs.WithLabelValues(name)
	r.statTotalRunningJobs.WithLabelValues(name)
	r.statTotalRegisteredRunners.WithLabelValues(name)
	r.statTotalBusyRunners.WithLabelValues(name)
	r.statTotalIdleRunners.WithLabelValues(name)
	return &ScaleSetRecorder{recorder: r, name: name}
}

// --- sslistener.MetricsRecorder ---------------------------------------

// RecordStatistics is invoked by the upstream listener with every
// poll-cycle statistics payload.
func (r *Recorder) RecordStatistics(s *ssapi.RunnerScaleSetStatistic) {
	r.ForScaleSet("").RecordStatistics(s)
}

func (r *ScaleSetRecorder) RecordStatistics(s *ssapi.RunnerScaleSetStatistic) {
	if s == nil {
		return
	}
	r.recorder.statTotalAvailableJobs.WithLabelValues(r.name).Set(float64(s.TotalAvailableJobs))
	r.recorder.statTotalAcquiredJobs.WithLabelValues(r.name).Set(float64(s.TotalAcquiredJobs))
	r.recorder.statTotalAssignedJobs.WithLabelValues(r.name).Set(float64(s.TotalAssignedJobs))
	r.recorder.statTotalRunningJobs.WithLabelValues(r.name).Set(float64(s.TotalRunningJobs))
	r.recorder.statTotalRegisteredRunners.WithLabelValues(r.name).Set(float64(s.TotalRegisteredRunners))
	r.recorder.statTotalBusyRunners.WithLabelValues(r.name).Set(float64(s.TotalBusyRunners))
	r.recorder.statTotalIdleRunners.WithLabelValues(r.name).Set(float64(s.TotalIdleRunners))
}

// RecordJobStarted bumps the started counter.
func (r *Recorder) RecordJobStarted(event *ssapi.JobStarted) {
	r.ForScaleSet("").RecordJobStarted(event)
}

func (r *ScaleSetRecorder) RecordJobStarted(_ *ssapi.JobStarted) {
	r.recorder.jobsStarted.WithLabelValues(r.name).Inc()
}

// RecordJobCompleted bumps the completed counter with GitHub's result.
func (r *Recorder) RecordJobCompleted(event *ssapi.JobCompleted) {
	r.ForScaleSet("").RecordJobCompleted(event)
}

func (r *ScaleSetRecorder) RecordJobCompleted(event *ssapi.JobCompleted) {
	result := "unknown"
	if event != nil && event.Result != "" {
		result = event.Result
	}
	r.recorder.jobsCompleted.WithLabelValues(r.name, result).Inc()
}

// RecordDesiredRunners records the most recent runner-count target.
func (r *Recorder) RecordDesiredRunners(count int) {
	r.ForScaleSet("").RecordDesiredRunners(count)
}

func (r *ScaleSetRecorder) RecordDesiredRunners(count int) {
	r.recorder.desiredRunners.WithLabelValues(r.name).Set(float64(count))
}

// --- incuse-side hooks ------------------------------------------------

// RunnerSpawned increments when the orchestrator has decided to mint
// a new idle runner in response to GitHub's desired-runner-count.
func (r *Recorder) RunnerSpawned() {
	r.ForScaleSet("").RunnerSpawned()
}

func (r *ScaleSetRecorder) RunnerSpawned() {
	r.recorder.runnersSpawned.WithLabelValues(r.name).Inc()
}

// LaunchOK / LaunchFail bucket Incus launch outcomes.
func (r *Recorder) LaunchOK()   { r.ForScaleSet("").LaunchOK() }
func (r *Recorder) LaunchFail() { r.ForScaleSet("").LaunchFail() }
func (r *ScaleSetRecorder) LaunchOK() {
	r.recorder.launches.WithLabelValues(r.name, "ok").Inc()
}
func (r *ScaleSetRecorder) LaunchFail() {
	r.recorder.launches.WithLabelValues(r.name, "fail").Inc()
}

// LaunchDuration observes the wall-clock cost of one launch.
func (r *Recorder) LaunchDuration(seconds float64) {
	r.ForScaleSet("").LaunchDuration(seconds)
}

func (r *ScaleSetRecorder) LaunchDuration(seconds float64) {
	r.recorder.launchDuration.WithLabelValues(r.name).Observe(seconds)
}

// RunnerLifetime observes total VM lifetime from JIT mint to
// termination (whatever the cause).
func (r *Recorder) RunnerLifetime(seconds float64) {
	r.ForScaleSet("").RunnerLifetime(seconds)
}

func (r *ScaleSetRecorder) RunnerLifetime(seconds float64) {
	r.recorder.runnerLifetime.WithLabelValues(r.name).Observe(seconds)
}

// Reap buckets a reaper termination by reason. Reasons used by the
// orchestrator: "registration_timeout", "max_job_duration",
// "drift_sweep", "job_completed".
func (r *Recorder) Reap(reason string) {
	r.ForScaleSet("").Reap(reason)
}

func (r *ScaleSetRecorder) Reap(reason string) {
	r.recorder.reaps.WithLabelValues(r.name, reason).Inc()
}

// SetTrackedInstances overwrites the tracker-size gauge.
func (r *Recorder) SetTrackedInstances(n int) {
	r.ForScaleSet("").SetTrackedInstances(n)
}

func (r *ScaleSetRecorder) SetTrackedInstances(n int) {
	r.recorder.trackedInstances.WithLabelValues(r.name).Set(float64(n))
}

// Discard returns a Recorder-shaped value that drops every observation
// on the floor. Useful in tests and when ListenAddr is empty.
func Discard() *Recorder {
	// A "discard" recorder still satisfies the interface; we just
	// never expose its registry. New() with empty version/commit is
	// fine — nothing scrapes it.
	return New("", "")
}
