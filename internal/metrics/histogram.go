package metrics

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/rm4n0s/errors"
)

// Histogram counts observations in buckets. Every method accepts a nil
// Histogram and does nothing. It is safe for concurrent use.
type Histogram struct {
	bounds []float64
	// counts[i] is the number of observations in (bounds[i-1], bounds[i]];
	// the last one is the observations above every bound.
	counts []atomic.Uint64
	// sum holds the bits of the float64 sum of the observations.
	sum atomic.Uint64
}

// newHistogram makes a histogram with the bounds, which the caller checked.
func newHistogram(bounds []float64) *Histogram {
	return &Histogram{bounds: bounds, counts: make([]atomic.Uint64, len(bounds)+1)}
}

// Observe records one value. A NaN is ignored.
func (h *Histogram) Observe(v float64) {
	if h == nil || math.IsNaN(v) {
		return
	}
	h.counts[sort.SearchFloat64s(h.bounds, v)].Add(1)
	for {
		old := h.sum.Load()
		if h.sum.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			return
		}
	}
}

// ObserveDuration records a duration in seconds.
func (h *Histogram) ObserveDuration(d time.Duration) {
	h.Observe(d.Seconds())
}

// HistogramVec is a histogram with labels. Every method accepts a nil
// HistogramVec.
type HistogramVec struct {
	v vec[Histogram]
}

// NewHistogramVec makes a histogram with labels and adds it to the registry.
// buckets are the upper bounds in seconds (or whatever the unit is), strictly
// increasing and finite; the +Inf bucket is added. It fails with
// tagBucketsInvalid for other bounds, and like NewCounterVec.
func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...Label) (*HistogramVec, error) {
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) || i > 0 && b <= buckets[i-1] {
			return nil, errors.New(tagBucketsInvalid, "the buckets must be finite and strictly increasing",
				"name", name, "bucket", b)
		}
	}
	bounds := append([]float64(nil), buckets...)
	d := &desc{name: name, help: help, kind: KindHistogram, labels: labels}
	c, err := r.register(d, func() collector {
		return &HistogramVec{v: newVec(d, func() *Histogram { return newHistogram(bounds) })}
	})
	if err != nil {
		return nil, err
	}
	hv, _ := c.(*HistogramVec)
	return hv, nil
}

// With returns the Histogram of the label values; see CounterVec.With.
func (hv *HistogramVec) With(values ...string) *Histogram {
	if hv == nil {
		return nil
	}
	return hv.v.with(values)
}

func (hv *HistogramVec) describe() *desc { return hv.v.d }

func (hv *HistogramVec) write(b *bytes.Buffer) {
	d := hv.v.d
	for _, e := range hv.v.snapshot() {
		var cum uint64
		for i, bound := range e.s.bounds {
			cum += e.s.counts[i].Load()
			writeSample(b, d.name+"_bucket", d.labels, e.k[:], "le", formatFloat(bound), strconv.FormatUint(cum, 10))
		}
		cum += e.s.counts[len(e.s.bounds)].Load()
		writeSample(b, d.name+"_bucket", d.labels, e.k[:], "le", "+Inf", strconv.FormatUint(cum, 10))
		writeSample(b, d.name+"_sum", d.labels, e.k[:], "", "", formatFloat(math.Float64frombits(e.s.sum.Load())))
		writeSample(b, d.name+"_count", d.labels, e.k[:], "", "", strconv.FormatUint(cum, 10))
	}
}
