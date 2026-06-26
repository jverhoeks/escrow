package ringbuf

import (
	"reflect"
	"testing"
)

func TestRingbuf_NewestOldest(t *testing.T) {
	b := New[int](3)
	if b.Len() != 0 || len(b.Newest()) != 0 {
		t.Fatal("empty buffer should have no items")
	}
	b.Push(1)
	b.Push(2)
	if !reflect.DeepEqual(b.Newest(), []int{2, 1}) {
		t.Errorf("Newest = %v, want [2 1]", b.Newest())
	}
	if !reflect.DeepEqual(b.Oldest(), []int{1, 2}) {
		t.Errorf("Oldest = %v, want [1 2]", b.Oldest())
	}
}

func TestRingbuf_OverwritesOldestAtCapacity(t *testing.T) {
	b := New[int](3)
	for i := 1; i <= 5; i++ { // 1,2 evicted; keeps 3,4,5
		b.Push(i)
	}
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3", b.Len())
	}
	if !reflect.DeepEqual(b.Newest(), []int{5, 4, 3}) {
		t.Errorf("Newest = %v, want [5 4 3]", b.Newest())
	}
	if !reflect.DeepEqual(b.Oldest(), []int{3, 4, 5}) {
		t.Errorf("Oldest = %v, want [3 4 5]", b.Oldest())
	}
}

func TestRingbuf_ZeroCapacityTreatedAsOne(t *testing.T) {
	b := New[int](0)
	b.Push(1)
	b.Push(2)
	if !reflect.DeepEqual(b.Newest(), []int{2}) {
		t.Errorf("Newest = %v, want [2]", b.Newest())
	}
}
