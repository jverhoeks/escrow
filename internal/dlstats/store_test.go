package dlstats_test

import (
	"path/filepath"
	"testing"

	"github.com/jverhoeks/escrow/internal/dlstats"
	"github.com/stretchr/testify/require"
)

func TestStore_IncrAndGet(t *testing.T) {
	s, err := dlstats.New("")
	require.NoError(t, err)
	s.Incr("npm", "lodash", "4.17.21")
	s.Incr("npm", "lodash", "4.17.21")
	st, ok := s.Get("npm", "lodash", "4.17.21")
	require.True(t, ok)
	require.Equal(t, 2, st.Count)
	require.False(t, st.FirstAt.IsZero())
	require.False(t, st.LastAt.IsZero())
	require.False(t, st.LastAt.Before(st.FirstAt))
}

func TestStore_PersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dl.json")
	s, err := dlstats.New(path)
	require.NoError(t, err)
	s.Incr("pypi", "requests", "2.31.0")
	require.NoError(t, s.Flush())

	s2, err := dlstats.New(path)
	require.NoError(t, err)
	st, ok := s2.Get("pypi", "requests", "2.31.0")
	require.True(t, ok)
	require.Equal(t, 1, st.Count)
}

func TestStore_GetMissing(t *testing.T) {
	s, _ := dlstats.New("")
	_, ok := s.Get("npm", "nope", "1.0.0")
	require.False(t, ok)
}
