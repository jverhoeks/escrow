package dlstats

import (
	"fmt"
	"testing"
)

// The store must bound its map rather than retaining one entry per version ever
// downloaded. See #50.
func TestStore_BoundedSize(t *testing.T) {
	s, _ := New("")
	for i := 0; i < maxEntries+50; i++ {
		s.Incr("npm", fmt.Sprintf("pkg-%d", i), "1.0.0")
	}
	s.mu.Lock()
	n := len(s.m)
	s.mu.Unlock()
	if n > maxEntries {
		t.Errorf("dlstats map grew to %d entries, want <= %d", n, maxEntries)
	}
}
