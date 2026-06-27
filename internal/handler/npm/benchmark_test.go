package npm_test

import (
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func BenchmarkSingleflight_Dedup(b *testing.B) {
	var g singleflight.Group

	b.ResetTimer()
	for range b.N {
		g.Do("key", func() (any, error) {
			time.Sleep(100 * time.Microsecond)
			return "ok", nil
		})
	}
}

func BenchmarkSingleflight_ConcurrentDedup(b *testing.B) {
	var g singleflight.Group
	concurrency := 10

	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		for range concurrency {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g.Do("key", func() (any, error) {
					time.Sleep(100 * time.Microsecond)
					return "ok", nil
				})
			}()
		}
		wg.Wait()
	}
}

func BenchmarkSingleflight_Sequential(b *testing.B) {
	var g singleflight.Group

	b.ResetTimer()
	for range b.N {
		g.Do("key", func() (any, error) {
			time.Sleep(100 * time.Microsecond)
			return "ok", nil
		})
	}
}

func BenchmarkSingleflight_NoDedup(b *testing.B) {
	// Baseline: same operation without any singleflight wrapper.
	b.ResetTimer()
	for range b.N {
		time.Sleep(100 * time.Microsecond)
	}
}

func BenchmarkSingleflight_Forget(b *testing.B) {
	var g singleflight.Group

	b.ResetTimer()
	for range b.N {
		g.Do("key", func() (any, error) {
			return "ok", nil
		})
		g.Forget("key")
	}
}
