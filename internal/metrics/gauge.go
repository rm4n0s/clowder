package metrics

import (
	"bytes"
	"strconv"
	"sync/atomic"
)

// Gauge is a whole number that goes up and down. Every method accepts a nil
// Gauge and does nothing. It is safe for concurrent use.
type Gauge struct {
	n atomic.Int64
}

// Inc adds one.
func (g *Gauge) Inc() {
	if g != nil {
		g.n.Add(1)
	}
}

// Dec subtracts one.
func (g *Gauge) Dec() {
	if g != nil {
		g.n.Add(-1)
	}
}

// Add adds n, which may be negative.
func (g *Gauge) Add(n int64) {
	if g != nil {
		g.n.Add(n)
	}
}

// Set sets the value.
func (g *Gauge) Set(n int64) {
	if g != nil {
		g.n.Store(n)
	}
}

// Value returns the value; 0 for a nil Gauge.
func (g *Gauge) Value() int64 {
	if g == nil {
		return 0
	}
	return g.n.Load()
}

// GaugeVec is a gauge with labels. Every method accepts a nil GaugeVec.
type GaugeVec struct {
	v vec[Gauge]
}

// NewGaugeVec makes a gauge with labels and adds it to the registry. It fails
// like NewCounterVec.
func (r *Registry) NewGaugeVec(name, help string, labels ...Label) (*GaugeVec, error) {
	d := &desc{name: name, help: help, kind: KindGauge, labels: labels}
	c, err := r.register(d, func() collector {
		return &GaugeVec{v: newVec(d, func() *Gauge { return &Gauge{} })}
	})
	if err != nil {
		return nil, err
	}
	gv, _ := c.(*GaugeVec)
	return gv, nil
}

// NewGauge makes a gauge with no labels and adds it to the registry.
func (r *Registry) NewGauge(name, help string) (*Gauge, error) {
	gv, err := r.NewGaugeVec(name, help)
	if err != nil {
		return nil, err
	}
	return gv.With(), nil
}

// With returns the Gauge of the label values; see CounterVec.With.
func (gv *GaugeVec) With(values ...string) *Gauge {
	if gv == nil {
		return nil
	}
	return gv.v.with(values)
}

// Preload makes the series of every combination of allowed values; see
// CounterVec.Preload.
func (gv *GaugeVec) Preload() {
	if gv != nil {
		gv.v.preload()
	}
}

func (gv *GaugeVec) describe() *desc { return gv.v.d }

func (gv *GaugeVec) write(b *bytes.Buffer) {
	for _, e := range gv.v.snapshot() {
		writeSample(b, gv.v.d.name, gv.v.d.labels, e.k[:], "", "", strconv.FormatInt(e.s.Value(), 10))
	}
}
