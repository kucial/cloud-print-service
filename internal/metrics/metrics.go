// Package metrics is a dependency-free Prometheus text-format registry.
//
// Deliberately tiny: the MVP needs a handful of counters/gauges, not a client library.
package metrics

import "sync"

type Registry struct {
	mu   sync.RWMutex
	vals map[string]float64
}

func New() *Registry { return &Registry{vals: map[string]float64{}} }

// Inc adds 1 to a counter series. labels are alternating key/value pairs.
func (r *Registry) Inc(name string, labels ...string) { r.Add(name, 1, labels...) }

// Add adds delta to a counter series.
func (r *Registry) Add(name string, delta float64, labels ...string) {
	key := series(name, labels)
	r.mu.Lock()
	r.vals[key] += delta
	r.mu.Unlock()
}

// Set writes a gauge series.
func (r *Registry) Set(name string, v float64, labels ...string) {
	key := series(name, labels)
	r.mu.Lock()
	r.vals[key] = v
	r.mu.Unlock()
}

// Observe records a sample into the `_sum` / `_count` pair of a summary metric.
func (r *Registry) Observe(name string, v float64, labels ...string) {
	r.Add(name+"_sum", v, labels...)
	r.Add(name+"_count", 1, labels...)
}

func series(name string, labels []string) string {
	if len(labels) == 0 {
		return name
	}
	out := name + "{"
	for i := 0; i+1 < len(labels); i += 2 {
		if i > 0 {
			out += ","
		}
		out += escape(labels[i]) + `="` + escape(labels[i+1]) + `"`
	}
	return out + "}"
}

func escape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// Render returns the registry in Prometheus text exposition format.
func (r *Registry) Render() string {
	r.mu.RLock()
	keys := make([]string, 0, len(r.vals))
	for k := range r.vals {
		keys = append(keys, k)
	}
	r.mu.RUnlock()
	sortStrings(keys)

	var b []byte
	for _, k := range keys {
		r.mu.RLock()
		v := r.vals[k]
		r.mu.RUnlock()
		b = append(b, k...)
		b = append(b, ' ')
		b = append(b, formatFloat(v)...)
		b = append(b, '\n')
	}
	return string(b)
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return itoa(int64(f))
	}
	// 4 decimals is plenty for gauges we expose.
	s := []byte{}
	neg := f < 0
	if neg {
		f = -f
	}
	mult := int64(f * 10000)
	whole := mult / 10000
	frac := mult % 10000
	s = itoaBytes(whole)
	s = append(s, '.')
	ds := itoa(frac)
	for i := len(ds); i < 4; i++ {
		s = append(s, '0')
	}
	s = append(s, ds...)
	if neg {
		return "-" + string(s)
	}
	return string(s)
}

func itoa(i int64) string { return string(itoaBytes(i)) }

func itoaBytes(i int64) []byte {
	if i == 0 {
		return []byte{'0'}
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return b[pos:]
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
