package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpus builds a tree of text files plus assets with binary extensions, in
// roughly the proportion a real checkout has: a lot of small source files and a
// smaller number of much larger assets. The assets are filled with plausible
// binary content so that, unskipped, they cost a real open and read before the
// NUL check rejects them.
func corpus(t testing.TB, textFiles, assets, assetSize int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < textFiles; i++ {
		dir := filepath.Join(root, "pkg", string(rune('a'+i%20)))
		os.MkdirAll(dir, 0o755)
		body := strings.Repeat("func handler(w, r) { return nil }\n", 40)
		p := filepath.Join(dir, "file"+itoa(i)+".go")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	blob := make([]byte, assetSize)
	for i := range blob {
		blob[i] = byte(i % 251) // includes NUL, as any real asset does
	}
	exts := []string{".png", ".ttf", ".woff2", ".jpg", ".pdf", ".zip"}
	for i := 0; i < assets; i++ {
		dir := filepath.Join(root, "assets")
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, "asset"+itoa(i)+exts[i%len(exts)])
		if err := os.WriteFile(p, blob, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The measurement the TODO asked for, and the reason it asked: a filter can
// cost more than the read it avoids.
//
// Both halves run over ONE corpus, as sub-benchmarks. Building a tree per
// benchmark makes the two numbers differ in page-cache state and inode layout
// as well as in the filter, which is enough to invent an 8% difference where
// there is none — that is exactly what a first cut of this measured.
//
//	go test ./internal/search/ -run XXX -bench SkipBinary
func BenchmarkSkipBinary(b *testing.B) {
	for _, c := range []struct {
		name         string
		text, assets int
	}{
		// A checkout with a large asset directory: the case the filter is for.
		{"with assets", 400, 60},
		// A checkout with none: the case it has to be free on, where every
		// file pays the check and no file benefits.
		{"no assets", 400, 0},
	} {
		root := corpus(b, c.text, c.assets, 256<<10)
		b.Run(c.name+"/on", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Run(root, Query{Text: "handler"})
			}
		})
		b.Run(c.name+"/off", func(b *testing.B) {
			defer disableBinarySkip()()
			for i := 0; i < b.N; i++ {
				Run(root, Query{Text: "handler"})
			}
		})
	}
}

// ---------- correctness ----------

// Assets are not searched, and text files still are.
func TestBinaryExtensionsAreSkipped(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("real.go", "needle here\n")
	write("asset.png", "needle here\n") // text content, binary extension
	write("archive.zip", "needle here\n")

	res := Run(root, Query{Text: "needle"})
	if res.Files != 1 {
		t.Errorf("%d files matched, want only the .go one", res.Files)
	}
	for _, m := range res.Matches {
		if filepath.Ext(m.Path) != ".go" {
			t.Errorf("searched %s", m.Path)
		}
	}
}

// The trap this list exists to avoid. These extensions look binary and are
// source far more often than not; skipping one makes a search silently wrong,
// which is a much worse failure than being slow.
func TestSourceExtensionsAreNotSkipped(t *testing.T) {
	for _, ext := range []string{
		".ts", ".h", ".m", ".cs", ".r", ".d", ".s", ".c", ".cc", ".go",
		".rs", ".py", ".rb", ".js", ".jsx", ".tsx", ".java", ".php", ".pl",
		".sh", ".md", ".txt", ".json", ".yaml", ".toml", ".sql", ".css",
	} {
		if skipBinary("file" + ext) {
			t.Errorf("%s is treated as binary; a search over it would silently miss", ext)
		}
	}
}

// Extensions are matched case-insensitively: a camera writes IMG_1234.JPG.
func TestBinaryExtensionsAreCaseInsensitive(t *testing.T) {
	for _, name := range []string{"a.PNG", "b.Jpeg", "c.TTF", "d.ZIP"} {
		if !skipBinary(name) {
			t.Errorf("%s was not skipped", name)
		}
	}
}

// Names with no extension, or with a dot that is not one, are searched. A
// Makefile and a README have no extension and are exactly the kind of file
// somebody greps.
func TestFilesWithoutExtensionsAreSearched(t *testing.T) {
	for _, name := range []string{"Makefile", "README", "go", "a.b.go"} {
		if skipBinary(name) {
			t.Errorf("%s was skipped", name)
		}
	}
}

// An unsaved buffer is searched from memory and never opened, so the extension
// rule must not apply to it: whatever the tab is called, its contents are text
// and are on screen.
func TestOpenDocumentsAreSearchedRegardless(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scratch.png")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := RunDocs(context.Background(), root, Query{Text: "needle"}, Docs{path: "needle here\n"})
	if res.Files != 1 {
		t.Errorf("%d files matched, want the open document", res.Files)
	}
}
