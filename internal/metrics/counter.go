package metrics

import (
	"bytes"
	"strconv"
	"sync/atomic"
)

// Counter is a value that only goes up. Every method accepts a nil Counter and
// does nothing. It is safe for concurrent use.
type Counter struct {
	n atomic.Uint64
}

// Inc adds one.
func (c *Counter) Inc() {
	if c != nil {
		c.n.Add(1)
	}
}

// Add adds n.
func (c *Counter) Add(n uint64) {
	if c != nil {
		c.n.Add(n)
	}
}

// Value returns the count; 0 for a nil Counter.
func (c *Counter) Value() uint64 {
	if c == nil {
		return 0
	}
	return c.n.Load()
}

// CounterVec is a counter with labels: one Counter for each combination of
// label values. Every method accepts a nil CounterVec.
type CounterVec struct {
	v vec[Counter]
}

// NewCounterVec makes a counter with labels and adds it to the registry. By
// the conventions of Prometheus its name ends in _total. It fails with
// tagMetricNameInvalid, tagMetricLabelInvalid or tagMetricDuplicate.
func (r *Registry) NewCounterVec(name, help string, labels ...Label) (*CounterVec, error) {
	d := &desc{name: name, help: help, kind: KindCounter, labels: labels}
	c, err := r.register(d, func() collector {
		return &CounterVec{v: newVec(d, func() *Counter { return &Counter{} })}
	})
	if err != nil {
		return nil, err
	}
	cv, _ := c.(*CounterVec)
	return cv, nil
}

// NewCounter makes a counter with no labels and adds it to the registry.
func (r *Registry) NewCounter(name, help string) (*Counter, error) {
	cv, err := r.NewCounterVec(name, help)
	if err != nil {
		return nil, err
	}
	return cv.With(), nil
}

// With returns the Counter of the label values, in the order of the labels. A
// value outside the allowed list of its label is counted as "other". With the
// wrong number of values it returns nil, which counts nothing.
func (cv *CounterVec) With(values ...string) *Counter {
	if cv == nil {
		return nil
	}
	return cv.v.with(values)
}

// Preload makes the series of every combination of allowed values, so that
// they are written as 0 before they are used. It does nothing for a metric
// with an open label.
func (cv *CounterVec) Preload() {
	if cv != nil {
		cv.v.preload()
	}
}

func (cv *CounterVec) describe() *desc { return cv.v.d }

func (cv *CounterVec) write(b *bytes.Buffer) {
	for _, e := range cv.v.snapshot() {
		writeSample(b, cv.v.d.name, cv.v.d.labels, e.k[:], "", "", strconv.FormatUint(e.s.Value(), 10))
	}
}
