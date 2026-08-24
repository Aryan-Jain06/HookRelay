// Package metrics holds the Prometheus collectors shared by the API and worker,
// and the HTTP listener that exposes them.
//
// The listener is deliberately separate from the API's own mux so metrics are
// never reachable through the public ingress alongside tenant data.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Attempts is the counter to alert on: a rising failure or skipped rate is
	// subscribers breaking.
	Attempts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hookrelay_delivery_attempts_total",
		Help: "Delivery attempts by outcome (success, failure, skipped, expired).",
	}, []string{"outcome"})

	// AttemptDuration measures how long the subscriber took to answer, which is
	// what a p95 delivery latency is actually made of.
	AttemptDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "hookrelay_attempt_duration_seconds",
		Help:    "Time spent waiting on the subscriber, by outcome.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"outcome"})

	// DeliveriesSettled counts deliveries reaching a terminal state. A rising
	// dead count is the single most important signal in the system: it means
	// deliveries were permanently abandoned.
	DeliveriesSettled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hookrelay_deliveries_settled_total",
		Help: "Deliveries reaching a terminal state (succeeded, dead).",
	}, []string{"status"})

	// EventsIngested counts accepted publishes, separating fresh events from
	// idempotency-key replays.
	EventsIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hookrelay_events_ingested_total",
		Help: "Events accepted at /events, by result (created, duplicate).",
	}, []string{"result"})

	// DeliveriesEnqueued counts fan-out.
	DeliveriesEnqueued = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hookrelay_deliveries_enqueued_total",
		Help: "Deliveries created and queued by fan-out.",
	})

	// StreamDepth is refreshed by a background sampler. Rising steadily means
	// the workers cannot keep up.
	StreamDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hookrelay_stream_depth",
		Help: "Entries currently in the Redis delivery stream.",
	})

	// PendingEntries is unacknowledged entries in the consumer group. A high,
	// sustained value means workers are claiming work and dying before
	// recording it.
	PendingEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hookrelay_stream_pending_entries",
		Help: "Unacknowledged entries in the consumer group.",
	})

	// Reclaimed counts work recovered by the reaper, by which path found it.
	Reclaimed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hookrelay_reclaimed_total",
		Help: "Work items recovered after a worker died, by path (stale_row, stream_entry).",
	}, []string{"path"})

	// RateLimited counts rejected requests, by which limiter rejected them.
	RateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hookrelay_rate_limited_total",
		Help: "Requests rejected by a rate limiter, by scope (tenant, ip).",
	}, []string{"scope"})

	// StreamTrimmed counts entries removed by the trimmer.
	StreamTrimmed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hookrelay_stream_trimmed_total",
		Help: "Stream entries removed by the trimmer.",
	})

	// AttemptsPruned counts delivery_attempts rows removed by retention.
	AttemptsPruned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hookrelay_attempts_pruned_total",
		Help: "delivery_attempts rows removed by the retention sweep.",
	})
)

// Serve runs the metrics listener until ctx is cancelled. An empty addr
// disables it. It never returns an error for a normal shutdown.
func Serve(ctx context.Context, addr string) error {
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("metrics listener starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// QueueSampler reports the stream's current depth and pending-entry count.
// Implemented by *queue.Queue; declared here so the metrics package does not
// import the queue.
type QueueSampler interface {
	Depth(ctx context.Context) (int64, error)
	PendingCount(ctx context.Context) (int64, error)
}

// SampleQueue refreshes the queue gauges on an interval until ctx is cancelled.
// Gauges cannot be derived from counters, so something has to poll them; doing
// it here keeps that concern out of the delivery path.
func SampleQueue(ctx context.Context, q QueueSampler, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if depth, err := q.Depth(sampleCtx); err == nil {
			StreamDepth.Set(float64(depth))
		} else if ctx.Err() == nil {
			slog.Warn("sample stream depth", "error", err)
		}
		if pending, err := q.PendingCount(sampleCtx); err == nil {
			PendingEntries.Set(float64(pending))
		} else if ctx.Err() == nil {
			slog.Warn("sample pending entries", "error", err)
		}
		cancel()

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
