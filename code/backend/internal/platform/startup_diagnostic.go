package platform

import "sync"

// A noisy server must not grow an unbounded stderr buffer. Only startup
// failures use this diagnostic; known credentials are redacted before return.
type startupDiagnostic struct {
	mu   sync.Mutex
	data []byte
}

func (d *startupDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	const limit = 8192
	n := len(p)
	if n >= limit {
		d.data = append(d.data[:0], p[n-limit:]...)
	} else {
		if extra := len(d.data) + n - limit; extra > 0 {
			d.data = append(d.data[:0], d.data[extra:]...)
		}
		d.data = append(d.data, p...)
	}
	return n, nil
}
func (d *startupDiagnostic) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.data)
}
