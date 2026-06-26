// Package ringbuf is a fixed-capacity, newest-first ring buffer shared by the
// in-memory observability logs (access, upstream, event). It replaces the
// previous `append([]T{e}, slice...)` prepend, which reallocated and copied the
// whole slice on every record — O(n) per insert and steady GC pressure on a
// busy instance. Push is O(1). See #71. Not safe for concurrent use; callers
// hold their own lock (they already do).
package ringbuf

// Buf is a fixed-capacity ring. Once full, the oldest item is overwritten.
type Buf[T any] struct {
	items []T
	head  int // index of the next write
	size  int // number of valid items (grows to cap, then stays)
}

// New returns a ring holding at most capacity items (capacity < 1 is treated as 1).
func New[T any](capacity int) *Buf[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Buf[T]{items: make([]T, capacity)}
}

// Push adds v as the newest item, overwriting the oldest once full.
func (b *Buf[T]) Push(v T) {
	b.items[b.head] = v
	b.head = (b.head + 1) % len(b.items)
	if b.size < len(b.items) {
		b.size++
	}
}

// Len returns the number of stored items.
func (b *Buf[T]) Len() int { return b.size }

// Newest returns a fresh slice of all items, newest first.
func (b *Buf[T]) Newest() []T {
	n := len(b.items)
	out := make([]T, b.size)
	for i := 0; i < b.size; i++ {
		out[i] = b.items[(b.head-1-i+2*n)%n]
	}
	return out
}

// Oldest returns a fresh slice of all items, oldest first.
func (b *Buf[T]) Oldest() []T {
	n := len(b.items)
	out := make([]T, b.size)
	// The oldest item is `size` steps behind head.
	start := (b.head - b.size + 2*n) % n
	for i := 0; i < b.size; i++ {
		out[i] = b.items[(start+i)%n]
	}
	return out
}

// Reset empties the buffer (and the new capacity, if cap > 0, resizes it).
func (b *Buf[T]) Reset(capacity int) {
	if capacity < 1 {
		capacity = len(b.items)
	}
	b.items = make([]T, capacity)
	b.head = 0
	b.size = 0
}
