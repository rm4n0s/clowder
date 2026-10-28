// Package metrics holds the counters, gauges and histograms of SPEC D60 and
// writes them in the Prometheus text format (version 0.0.4), by hand, with no
// client library.
//
// A Registry owns the metrics and writes all of them, sorted by name and then
// by label values. A metric is made with a fixed list of label names. A label
// that has an allowed list folds every other value into "other", so that its
// values stay a closed set; a label with no list takes the value as it comes
// and is only for values the caller bounds (the route pattern of the
// http.ServeMux). No label carries a user, an ID, a path with an ID, an
// address or a token (SPEC D60).
//
// Nothing here is global: serve makes one Registry and one Set and passes
// them to what reports into them. A nil Counter, Gauge, Histogram or vec
// accepts every call and does nothing, so a package that was given no metrics
// reports into nothing at no cost.
package metrics
