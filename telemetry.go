package main

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/charmbracelet/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Telemetry is a struct which contains the metrics of the application
type Telemetry struct {
	eventStreamConnected atomic.Bool
	nodesManaged         prometheus.Counter
	jobsDeployed         prometheus.Counter
}

// NewTelemetry is a function which returns a new telemetry instance and an error.
func NewTelemetry(ctx context.Context) (*Telemetry, error) {
	// Create open telemetry exporter

	t := &Telemetry{
		nodesManaged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nomad_step_up_nodes_managed",
			Help: "Number of nodes being managed",
		}),
		jobsDeployed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nomad_step_up_jobs_deployed",
			Help: "Number of jobs deployed",
		}),
	}

	prometheus.MustRegister(t.nodesManaged)
	prometheus.MustRegister(t.jobsDeployed)
	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "nomad_step_up_event_stream_up",
			Help: "Whether we can read the Nomad Event Stream",
		},
		func() float64 {
			if t.eventStreamConnected.Load() {
				return 1
			}
			return 0
		},
	))

	go func() {
		metricsPort := "0.0.0.0:8080"
		http.Handle("/metrics", promhttp.Handler())
		log.Infof("Starting Metrics Server on %s", metricsPort)
		if err := http.ListenAndServe(metricsPort, nil); err != nil {
			log.Errorf("failed to start metrics server: %v", err)
		}
	}()

	return t, nil
}

// RecordEventStreamStatus is a function to record the event stream status
func (t *Telemetry) RecordEventStreamStatus(ctx context.Context, connected bool) {
	t.eventStreamConnected.Store(connected)
}

// RecordNodeManaged is a function which increments the nodesManaged metric
func (t *Telemetry) RecordNodeManaged(ctx context.Context) {
	t.nodesManaged.Inc()
}

// RecordJobDeployed is a function which increments the jobsDeployed metric
func (t *Telemetry) RecordJobDeployed(ctx context.Context) {
	t.jobsDeployed.Inc()
}

// Shutdown is a function which takes a context and returns an optional errror.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	return nil
}
