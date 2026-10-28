package metrics

import (
	"bytes"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rm4n0s/errors"
)

// Kind says how a metric behaves, as the TYPE line of the text format.
type Kind string

// The kinds of metric.
const (
	// KindCounter is a value that only goes up.
	KindCounter Kind = "counter"
	// KindGauge is a value that goes up and down.
	KindGauge Kind = "gauge"
	// KindHistogram is a distribution in buckets.
	KindHistogram Kind = "histogram"
)

const (
	// maxLabels is the most labels a metric has. It lets a series be found by
	// an array key, with no allocation on the request path.
	maxLabels = 4
	// maxLabelValue is the longest label value that is kept, in bytes.
	maxLabelValue = 200
	// maxSeries is the most series a metric keeps. A label that has no allowed
	// list is bounded by its caller; this is the stop if the caller is wrong:
	// a new series beyond it is not recorded.
	maxSeries = 5000
	// otherValue replaces a value that is not in the allowed list of a label.
	otherValue = "other"
)

var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Label is the name of a label of a metric and, for a closed set of values,
// the values it may take. A value outside Allowed is written as "other".
// With no Allowed the value is taken as it is, so only a caller that bounds
// its values (the route pattern of the mux) uses that.
type Label struct {
	// Name is the label name: letters, digits and underscores, not starting
	// with a digit or with "__".
	Name string
	// Allowed is the closed set of values; empty means any value.
	Allowed []string
}

// L makes a Label. With allowed values it is a closed set; without, it is
// open.
func L(name string, allowed ...string) Label {
	return Label{Name: name, Allowed: allowed}
}

// desc is what every metric has: its name, help, kind and labels.
type desc struct {
	name   string
	help   string
	kind   Kind
	labels []Label
	// allowed holds, for each label, its set of allowed values; nil for an
	// open label.
	allowed []map[string]struct{}
}

// key is the label values of a series; the unused places stay empty.
type key [maxLabels]string

// key folds values into the key of a series: a value outside the allowed list
// of its label becomes "other", a value that is not UTF-8 or is too long is
// cleaned. It reports false when the number of values is not the number of
// labels.
func (d *desc) key(values []string) (key, bool) {
	var k key
	if len(values) != len(d.labels) {
		return k, false
	}
	for i, v := range values {
		if set := d.allowed[i]; set != nil {
			if _, ok := set[v]; !ok {
				v = otherValue
			}
		} else if len(v) > maxLabelValue || !utf8.ValidString(v) {
			v = cleanValue(v)
		}
		k[i] = v
	}
	return k, true
}

// cleanValue cuts v to maxLabelValue bytes and replaces what is not UTF-8.
func cleanValue(v string) string {
	if len(v) > maxLabelValue {
		v = v[:maxLabelValue]
	}
	return strings.ToValidUTF8(v, "�")
}

// collector is a metric the registry writes.
type collector interface {
	describe() *desc
	// write appends the samples of the metric, without its HELP and TYPE
	// lines, sorted by label values.
	write(b *bytes.Buffer)
}

// Registry owns the metrics and writes them. Make it with NewRegistry. It is
// safe for concurrent use.
type Registry struct {
	mu     sync.Mutex
	byName map[string]collector
}

// NewRegistry makes an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]collector{}}
}

// register checks the description, makes the collector with build and adds it.
// It fails with tagMetricNameInvalid, tagMetricLabelInvalid or
// tagMetricDuplicate; these are mistakes of the program, found when it
// starts.
func (r *Registry) register(d *desc, build func() collector) (collector, error) {
	if !metricNameRE.MatchString(d.name) {
		return nil, errors.New(tagMetricNameInvalid, "the metric name is not valid", "name", d.name)
	}
	if len(d.labels) > maxLabels {
		return nil, errors.New(tagMetricLabelInvalid, "a metric has too many labels", "name", d.name, "max", maxLabels)
	}
	seen := map[string]bool{}
	d.allowed = make([]map[string]struct{}, len(d.labels))
	for i, l := range d.labels {
		if !labelNameRE.MatchString(l.Name) || strings.HasPrefix(l.Name, "__") || seen[l.Name] ||
			d.kind == KindHistogram && l.Name == "le" {
			return nil, errors.New(tagMetricLabelInvalid, "the label name is not valid or is used twice",
				"name", d.name, "label", l.Name)
		}
		seen[l.Name] = true
		if len(l.Allowed) > 0 {
			d.allowed[i] = make(map[string]struct{}, len(l.Allowed))
			for _, v := range l.Allowed {
				d.allowed[i][v] = struct{}{}
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[d.name]; dup {
		return nil, errors.New(tagMetricDuplicate, "a metric with this name exists", "name", d.name)
	}
	c := build()
	r.byName[d.name] = c
	return c, nil
}

// WriteTo writes every metric in the Prometheus text format, sorted by metric
// name and then by label values. Metrics that are read when scraped run their
// functions here, outside the lock of the registry. It fails with
// tagWriteFailed when w does.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	list := make([]collector, 0, len(r.byName))
	for _, c := range r.byName {
		list = append(list, c)
	}
	r.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].describe().name < list[j].describe().name })
	var b bytes.Buffer
	for _, c := range list {
		d := c.describe()
		b.WriteString("# HELP ")
		b.WriteString(d.name)
		b.WriteByte(' ')
		b.WriteString(escapeHelp(d.help))
		b.WriteString("\n# TYPE ")
		b.WriteString(d.name)
		b.WriteByte(' ')
		b.WriteString(string(d.kind))
		b.WriteByte('\n')
		c.write(&b)
	}
	n, err := w.Write(b.Bytes())
	if err != nil {
		return int64(n), errors.NewErr(tagWriteFailed, err)
	}
	return int64(n), nil
}

// escapeHelp escapes a HELP text: a backslash and a newline.
func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// escapeValue escapes a label value: a backslash, a double quote and a
// newline.
func escapeValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// formatFloat writes a sample value or a bucket bound: integers without a
// fraction, +Inf, -Inf and NaN as the format spells them.
func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// writeSample appends one line: name{label="value",...} value. extra is an
// additional label written last (the le of a bucket); its name is empty when
// there is none.
func writeSample(b *bytes.Buffer, name string, labels []Label, values []string, extraName, extraValue, value string) {
	b.WriteString(name)
	if len(labels) > 0 || extraName != "" {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.Name)
			b.WriteString(`="`)
			b.WriteString(escapeValue(values[i]))
			b.WriteByte('"')
		}
		if extraName != "" {
			if len(labels) > 0 {
				b.WriteByte(',')
			}
			b.WriteString(extraName)
			b.WriteString(`="`)
			b.WriteString(escapeValue(extraValue))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(value)
	b.WriteByte('\n')
}

// keyLess orders two keys by their values, label by label.
func keyLess(a, b key) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

const (
	tagMetricNameInvalid  = "MetricNameInvalid"
	tagMetricLabelInvalid = "MetricLabelInvalid"
	tagMetricDuplicate    = "MetricDuplicate"
	tagMetricKindInvalid  = "MetricKindInvalid"
	tagBucketsInvalid     = "MetricBucketsInvalid"
	tagWriteFailed        = "MetricsWriteFailed"
)
