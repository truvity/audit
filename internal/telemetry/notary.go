package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Notary is what the notary reports.
//
// The notary is a scheduled job, so it pushes what it saw when it last ran and
// then goes away; the series stops with it. That is why SealAge is read with the
// time since its last sample added (see AuditSealStale in the chart): a notary
// that no longer runs shows as an age that keeps climbing and not as a series
// that disappeared.
type Notary struct {
	age      metric.Float64Gauge
	sealed   metric.Int64Counter
	failures metric.Int64Counter
}

// NewNotary makes the notary's instruments on the given provider, normally the
// global one Start installed.
func NewNotary(provider metric.MeterProvider) (*Notary, error) {
	m := provider.Meter("github.com/truvity/audit/notary")
	var n Notary
	var err error
	if n.age, err = m.Float64Gauge("audit.seal.age", metric.WithUnit("s"), // audit:not-an-action — a metric name
		metric.WithDescription("Seconds since the end of the newest sealed hour, per profile, taken of the tenant "+
			"furthest behind. Reported by each notary run, so the last value is as of that run.")); err != nil {
		return nil, fmt.Errorf("telemetry: audit.seal.age: %w", err)
	}
	if n.sealed, err = m.Int64Counter("audit.seal.written", metric.WithUnit("{seal}"), // audit:not-an-action — a metric name
		metric.WithDescription("Seals the notary signed and wrote, per profile.")); err != nil {
		return nil, fmt.Errorf("telemetry: audit.seal.written: %w", err)
	}
	if n.failures, err = m.Int64Counter("audit.seal.failures", metric.WithUnit("{tenant}"), // audit:not-an-action — a metric name
		metric.WithDescription("Tenants a notary run could not seal further, per profile: an object that does not match "+
			"its metadata, a seal that cannot be read, a signer that fails.")); err != nil {
		return nil, fmt.Errorf("telemetry: audit.seal.failures: %w", err)
	}
	return &n, nil
}

// Age records the age of a profile's newest sealed hour.
func (n *Notary) Age(profile string, seconds float64) {
	n.age.Record(context.Background(), seconds, metric.WithAttributes(attribute.String("profile", profile)))
}

// Sealed counts seals written for a profile.
func (n *Notary) Sealed(profile string, seals int) {
	n.sealed.Add(context.Background(), int64(seals), metric.WithAttributes(attribute.String("profile", profile)))
}

// Failed counts one tenant of a profile the run could not seal further.
func (n *Notary) Failed(profile string) {
	n.failures.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile)))
}
