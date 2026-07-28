package audio

import "sync"

// Ring is a bounded, thread-safe byte ring buffer. Audio written by the
// playback endpoint is read by the capture endpoint of the same virtual cable.
type Ring struct {
	mu        sync.Mutex
	buf       []byte
	read      int
	write     int
	size      int
	dropped   uint64
	underruns uint64
}

func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]byte, capacity)}
}

func (r *Ring) Capacity() int { return len(r.buf) }

func (r *Ring) Available() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

func (r *Ring) Stats() (available int, dropped uint64, underruns uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size, r.dropped, r.underruns
}

// Write keeps the newest audio. If the ring is full, the oldest bytes are
// discarded so the stream can continue instead of blocking the USB worker.
func (r *Ring) Write(p []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		if r.size == len(r.buf) {
			r.read = (r.read + 1) % len(r.buf)
			r.size--
			r.dropped++
		}
		r.buf[r.write] = b
		r.write = (r.write + 1) % len(r.buf)
		r.size++
	}
	return len(p)
}

// ReadSilence reads len(p) bytes. Missing audio is filled with digital silence
// (zero-valued PCM samples), which is preferable to stalling an isochronous URB.
func (r *Ring) ReadSilence(p []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	read := 0
	for i := range p {
		if r.size == 0 {
			p[i] = 0
			continue
		}
		p[i] = r.buf[r.read]
		r.read = (r.read + 1) % len(r.buf)
		r.size--
		read++
	}
	if read < len(p) {
		r.underruns += uint64(len(p) - read)
	}
	return read
}

func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.read, r.write, r.size = 0, 0, 0
}
