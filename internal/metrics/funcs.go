package metrics

import (
	"bytes"
	"sort"

	"github.com/rm4n0s/errors"
)

// Sample is one value of a metric that is read when it is scraped, with its
// label values in the order of the labels of the metric.
type Sample struct {
	// Labels are the label values.
	Labels []string
	// Value is the value.
	Value float64
}

// funcMetric is a counter or a gauge whose values come from a function that
// runs when the registry is written. Nothing samples in the background.
type funcMetric struct {
	d  *desc
	fn func() []Sample
}

// NewFunc adds a counter or gauge whose samples fn returns each time the
// registry is written. labelNames name the labels of its samples (no allowed
// lists: the function is the program's own). A sample with the wrong number of
// label values is left out. It fails with tagMetricKindInvalid for any kind
// but KindCounter and KindGauge, and like NewCounterVec.
func (r *Registry) NewFunc(kind Kind, name, help string, labelNames []string, fn func() []Sample) error {
	if kind != KindCounter && kind != KindGauge {
		return errors.New(tagMetricKindInvalid, "a function metric is a counter or a gauge", "name", name, "kind", string(kind))
	}
	labels := make([]Label, len(labelNames))
	for i, n := range labelNames {
		labels[i] = Label{Name: n}
	}
	d := &desc{name: name, help: help, kind: kind, labels: labels}
	_, err := r.register(d, func() collector { return &funcMetric{d: d, fn: fn} })
	return err
}

// NewGaugeFunc adds a gauge with no labels whose value is read when scraped.
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) error {
	return r.NewFunc(KindGauge, name, help, nil, func() []Sample { return []Sample{{Value: fn()}} })
}

// NewCounterFunc adds a counter with no labels whose value is read when
// scraped.
func (r *Registry) NewCounterFunc(name, help string, fn func() float64) error {
	return r.NewFunc(KindCounter, name, help, nil, func() []Sample { return []Sample{{Value: fn()}} })
}

func (f *funcMetric) describe() *desc { return f.d }

func (f *funcMetric) write(b *bytes.Buffer) {
	samples := f.fn()
	valid := samples[:0:0]
	for _, s := range samples {
		if len(s.Labels) == len(f.d.labels) {
			valid = append(valid, s)
		}
	}
	sort.SliceStable(valid, func(i, j int) bool {
		a, c := valid[i].Labels, valid[j].Labels
		for n := range a {
			if a[n] != c[n] {
				return a[n] < c[n]
			}
		}
		return false
	})
	for _, s := range valid {
		values := make([]string, len(s.Labels))
		for i, v := range s.Labels {
			values[i] = cleanValue(v)
		}
		writeSample(b, f.d.name, f.d.labels, values, "", "", formatFloat(s.Value))
	}
}
