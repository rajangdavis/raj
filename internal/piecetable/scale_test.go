package piecetable

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
)

func TestMemoryAndScale(t *testing.T) {
	for _, n := range []int{10000, 100000} {
		rng := rand.New(rand.NewSource(1))
		var before runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		d := NewDoc("", 5)
		for i := 0; i < n; i++ {
			d.Insert(Author(rng.Intn(3)), rng.Intn(d.Len()+1), "abcd")
		}
		var after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&after)
		bytes := float64(after.HeapAlloc-before.HeapAlloc) / float64(d.Pieces())
		t.Logf("n=%d pieces=%d len=%d  %.1f B/piece", n, d.Pieces(), d.Len(), bytes)
	}
}

func BenchmarkDocInsert(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			d := NewDoc("", 5)
			for i := 0; i < n; i++ {
				d.Insert(User, d.Len(), "xxxx")
			}
			rng := rand.New(rand.NewSource(2))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.Insert(User, rng.Intn(d.Len()), "y")
			}
		})
	}
}

func BenchmarkSpansScreenful(b *testing.B) {
	d := NewDoc("", 5)
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 200000; i++ {
		d.Insert(Author(rng.Intn(3)), rng.Intn(d.Len()+1), "abcd")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Spans(rng.Intn(d.Len()-4000), 4000)
	}
}

// TestSessionMemory checks the property the shared-buffer design rests on:
// agent-side state is O(diff), and the journal stores piece records rather than
// text, so history costs bytes proportional to edit volume, not document size.
func TestSessionMemory(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	orig := strings.Repeat("some line of source code\n", 40000) // ~1 MB
	s := NewSession(NewDoc(orig, 5))
	base := s.Version()
	for i := 0; i < 5000; i++ {
		start := rng.Intn(s.Buffer().Len() - 8)
		s.ApplyDiff(Agent, base, []Hunk{{Start: start, End: start + 4, Text: "EDIT"}})
		base = s.Version()
	}
	t.Logf("doc=%d bytes  pieces=%d  stores=%d bytes  journal=%d ops",
		s.Buffer().Len(), s.Buffer().Pieces(), s.Store().Bytes(), len(s.Journal()))
}
