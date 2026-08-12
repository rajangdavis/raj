package complete

import (
	"strings"
	"testing"
)

// Completion is recomputed on every keystroke, so the question is whether a
// realistic set of open buffers can be scanned inside a frame.
func BenchmarkRank(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString("func handleSomething(writer http.ResponseWriter) error {\n")
		sb.WriteString("\treturn processRequest(writer, handlerConfig)\n}\n\n")
	}
	src := sb.String()

	bufs := Buffers{Current: "/w/a.go", Contents: map[string]string{}}
	for _, n := range []string{"/w/a.go", "/w/b.go", "/w/c.go", "/w/d.go", "/w/e.go"} {
		bufs.Contents[n] = src
	}
	b.SetBytes(int64(len(src) * len(bufs.Contents)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bufs.Rank("hand")
	}
}
