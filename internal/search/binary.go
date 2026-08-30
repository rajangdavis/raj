package search

import (
	"path/filepath"
	"strings"
)

// Skipping files that cannot contain text.
//
// A search reads every eligible file to discover whether it is binary, which
// means opening a font or a PNG and pulling 64 KB off disk to find a NUL byte.
// On a checkout with a large asset directory that is most of the read budget
// spent on files that could never match.
//
// The list is by extension, which is a guess rather than a fact — a `.png` full
// of text would be skipped. That is the right trade for a workspace search
// (nobody greps their font files) and it is why this is a fixed list of
// unambiguous formats rather than anything clever.
//
// THE TRAP HERE IS EXTENSIONS THAT LOOK BINARY AND ARE NOT. `.ts` is
// TypeScript far more often than MPEG transport stream; `.h`, `.m`, `.cs`,
// `.r`, `.d` and `.s` are all source. None of them are listed, and none should
// be: skipping a source file makes a search silently wrong, while failing to
// skip an asset only makes it slower.

// binaryExts are formats whose contents are never text worth searching.
var binaryExts = map[string]bool{
	// Images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".ico": true, ".icns": true, ".webp": true, ".tiff": true, ".tif": true,
	".avif": true, ".heic": true, ".psd": true, ".xcf": true,
	// Fonts
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".ttc": true,
	// Archives and compressed streams
	".zip": true, ".gz": true, ".bz2": true, ".xz": true, ".zst": true,
	".7z": true, ".rar": true, ".tar": true, ".tgz": true, ".lz4": true,
	// Audio and video
	".mp3": true, ".mp4": true, ".m4a": true, ".m4v": true, ".wav": true,
	".flac": true, ".ogg": true, ".opus": true, ".webm": true, ".mov": true,
	".avi": true, ".mkv": true, ".wmv": true,
	// Compiled output and libraries
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true,
	".o": true, ".obj": true, ".lib": true, ".class": true, ".jar": true,
	".wasm": true, ".pyc": true, ".pyo": true, ".rlib": true, ".rmeta": true,
	// Documents and disk images
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".odt": true, ".ods": true,
	".iso": true, ".dmg": true, ".img": true, ".deb": true, ".rpm": true,
	".pkg": true, ".msi": true,
	// Databases and opaque blobs
	".sqlite": true, ".sqlite3": true, ".db": true, ".mo": true,
	".bin": true, ".dat": true, ".pack": true, ".idx": true,
}

// skipBinary reports whether a file name is one of the formats above.
//
// A map lookup on a lowered extension, which is why this is worth doing at all:
// the check has to be cheaper than the open and read it avoids by a wide enough
// margin that it is free on the files it does not skip. See BENCHMARKS.md — a
// filter costing more than the read it saves is not a hypothetical here, it is
// what the glob measurement found.
func skipBinary(name string) bool {
	if !binarySkipEnabled {
		return false
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	if binaryExts[ext] {
		return true
	}
	// Lowered only when the fast path misses, so the common case — an
	// already-lowercase extension, or one not on the list at all — never
	// allocates.
	lower := strings.ToLower(ext)
	return lower != ext && binaryExts[lower]
}

// disableBinarySkip turns the filter off and returns a function that restores
// it. It exists so the benchmarks can measure the same corpus with and without,
// in one process, differing in one thing only — the same seam forceRegexp
// provides for the literal fast path.
var binarySkipEnabled = true

func disableBinarySkip() func() {
	binarySkipEnabled = false
	return func() { binarySkipEnabled = true }
}
