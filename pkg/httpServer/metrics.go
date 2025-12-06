package httpServer

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	totalRequests    *prometheus.CounterVec
	durationSec      *prometheus.HistogramVec
	inflightRequests prometheus.Gauge
}

func (m *metrics) metricsMiddleware(ctx *fiber.Ctx) (err error) {
	m.inflightRequests.Inc()
	s := time.Now()
	defer func() {
		m.inflightRequests.Dec()
	}()

	err = ctx.Next()

	routeLabel := "<unmatched>"
	if r := ctx.Route(); r != nil && r.Path != "" {
		routeLabel = r.Path
	}

	labels := []string{
		routeLabel,
		string(ctx.Context().Method()),
		strconv.Itoa(ctx.Context().Response.StatusCode()),
	}

	m.totalRequests.WithLabelValues(labels...).Inc()
	m.durationSec.WithLabelValues(labels...).Observe(time.Since(s).Seconds())

	return
}

func newMetrics(namespace, subsystem string) *metrics {
	labels := []string{"route", "method", "code"}

	t := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "requests_total",
		Help:      "Total number of requests",
	}, labels)
	d := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "requests_duration",
		Help:      "Duration of requests",
	}, labels)
	i := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "requests_inflight",
		Help:      "Number of inflight requests",
	})

	prometheus.MustRegister(
		t,
		d,
		i,
	)

	return &metrics{
		totalRequests:    t,
		durationSec:      d,
		inflightRequests: i,
	}
}
