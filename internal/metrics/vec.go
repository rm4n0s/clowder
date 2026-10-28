package metrics

import (
	"sort"
	"sync"
)

// vec holds the series of a metric, one per combination of label values. A
// series is made the first time its values are used.
type vec[T any] struct {
	d      *desc
	mk     func() *T
	mu     sync.RWMutex
	series map[key]*T
}

// newVec makes an empty vec; mk makes a new series.
func newVec[T any](d *desc, mk func() *T) vec[T] {
	return vec[T]{d: d, mk: mk, series: map[key]*T{}}
}

// with returns the series of the values, making it if needed. It returns nil
// when the number of values is not the number of labels, or when the metric
// already has maxSeries series.
func (v *vec[T]) with(values []string) *T {
	k, ok := v.d.key(values)
	if !ok {
		return nil
	}
	v.mu.RLock()
	s := v.series[k]
	v.mu.RUnlock()
	if s != nil {
		return s
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s = v.series[k]; s != nil {
		return s
	}
	if len(v.series) >= maxSeries {
		return nil
	}
	s = v.mk()
	v.series[k] = s
	return s
}

// preload makes the series of every combination of the allowed values, so that
// a counter that has not moved yet is written as 0. It does nothing unless
// every label has an allowed list.
func (v *vec[T]) preload() {
	for _, set := range v.d.allowed {
		if set == nil {
			return
		}
	}
	combos := [][]string{{}}
	for _, l := range v.d.labels {
		var next [][]string
		for _, c := range combos {
			for _, val := range l.Allowed {
				next = append(next, append(append([]string(nil), c...), val))
			}
		}
		combos = next
	}
	for _, c := range combos {
		v.with(c)
	}
}

// entry is a series with its key.
type entry[T any] struct {
	k key
	s *T
}

// snapshot returns the series sorted by their label values.
func (v *vec[T]) snapshot() []entry[T] {
	v.mu.RLock()
	out := make([]entry[T], 0, len(v.series))
	for k, s := range v.series {
		out = append(out, entry[T]{k, s})
	}
	v.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return keyLess(out[i].k, out[j].k) })
	return out
}
