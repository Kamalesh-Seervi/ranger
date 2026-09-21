package core

// ring is a fixed-capacity circular buffer of signal samples. It never grows,
// which bounds memory when a device is seen for hours.
type ring struct {
	buf   []Sample
	next  int
	count int
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]Sample, capacity)}
}

func (r *ring) add(s Sample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// samples returns a copy ordered oldest to newest.
func (r *ring) samples() []Sample {
	if r.count == 0 {
		return nil
	}
	out := make([]Sample, 0, r.count)
	start := r.next - r.count
	if start < 0 {
		start += len(r.buf)
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
