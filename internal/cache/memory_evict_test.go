package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The memory backend must bound its meta map under a long-running diverse-key
// workload rather than retaining one entry per key forever. See #44.
func TestMemory_MetaMapBounded(t *testing.T) {
	m := NewMemory()
	defer m.Close()
	ctx := context.Background()
	for i := 0; i < metaCacheCapacity*3; i++ {
		_ = m.SetMeta(ctx, fmt.Sprintf("npm/meta/pkg-%d", i), []byte("x"), time.Hour)
	}
	m.mu.RLock()
	n := len(m.meta)
	m.mu.RUnlock()
	if n > metaCacheCapacity {
		t.Errorf("meta map grew to %d entries, want <= %d", n, metaCacheCapacity)
	}
}

// Eviction prefers entries already expired beyond the stale grace window, so a
// live (unexpired) entry survives eviction triggered by churn of dead keys.
func TestMemory_EvictDropsExpiredFirst(t *testing.T) {
	m := NewMemory()
	defer m.Close()
	ctx := context.Background()
	// One live entry we expect to survive.
	_ = m.SetMeta(ctx, "live", []byte("keep"), time.Hour)
	// Fill past capacity with already-expired entries.
	for i := 0; i < metaCacheCapacity*2; i++ {
		_ = m.SetMeta(ctx, fmt.Sprintf("dead-%d", i), []byte("x"), -time.Hour)
	}
	if v, _ := m.GetMeta(ctx, "live"); string(v) != "keep" {
		t.Errorf("live entry evicted before expired ones; got %q", v)
	}
}
