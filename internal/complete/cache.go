package complete

// Rescanning every open buffer on every keystroke is what the naive version
// did, and it was measured at 5.3 ms against 2 MB of open buffers — a third of
// a frame, on the keystroke path that raj otherwise keeps clear on principle.
//
// The scan itself is fine at ~300 MB/s. The waste is rescanning buffers that
// have not changed, which is nearly all of them nearly always: typing modifies
// one buffer and leaves the others exactly as they were.
//
// So the word set is cached per buffer and thrown away when that buffer's
// version moves. The version is the same key `textDocument/didChange` needs, so
// the cache and the language server want the identical thing — which is the
// reason to build it before the LSP work rather than after.

// Version identifies a buffer's contents. Any monotonic counter works; the
// editor supplies its session version. Zero is a legitimate version (an
// untouched buffer), so presence in the map is what marks an entry as valid
// rather than a zero check.
type Version int

// Cache holds the words of each open buffer, keyed by path and version.
//
// It is not safe for concurrent use. Every caller is the event thread, which is
// also where the buffers themselves may only be read, so a mutex here would
// guard against a caller that cannot exist.
type Cache struct {
	entries map[string]cacheEntry
}

type cacheEntry struct {
	version Version
	words   []string
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{entries: map[string]cacheEntry{}} }

// Words returns the words of the buffer at path, scanning only when the cached
// version does not match.
//
// The scan is passed as a function rather than the text, so an unchanged buffer
// costs nothing at all: materialising a piece table to hand over text that is
// then discarded would put back most of what the cache saves.
func (c *Cache) Words(path string, v Version, scan func() string) []string {
	if c.entries == nil {
		c.entries = map[string]cacheEntry{}
	}
	if e, ok := c.entries[path]; ok && e.version == v {
		return e.words
	}
	set := Words(scan(), nil)
	words := make([]string, 0, len(set))
	for w := range set {
		words = append(words, w)
	}
	// Sorted so the caller's ranking is fed a stable order. Ranking sorts by
	// score anyway, but ties are broken by what it saw first, and map order
	// would make that vary between identical keystrokes.
	sortStrings(words)
	c.entries[path] = cacheEntry{version: v, words: words}
	return words
}

// Retain drops buffers that are no longer open, so a long session does not hold
// the words of every file ever visited.
func (c *Cache) Retain(open map[string]bool) {
	for path := range c.entries {
		if !open[path] {
			delete(c.entries, path)
		}
	}
}

// Len is how many buffers are cached, for tests.
func (c *Cache) Len() int { return len(c.entries) }

// sortStrings is an insertion sort, which beats the general sort for the small,
// nearly-sorted slices this produces and avoids the reflection in sort.Slice on
// a path that runs per keystroke.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Snapshot is a buffer as the cache sees it: where it is, what version it is at,
// and how to read it if that version is new.
type Snapshot struct {
	Path    string
	Version Version
	Text    func() string
	Symbols []string
	Current bool
}

// Rank builds the candidate list from snapshots, scanning only what changed.
//
// This is the cached counterpart to Buffers.Rank and produces the same
// ordering, which is asserted rather than assumed: the two would otherwise
// drift, and the uncached one is what the ranking tests are written against.
func (c *Cache) Rank(snaps []Snapshot, prefix string) []Candidate {
	if len(prefix) < MinPrefix {
		return nil
	}
	best := map[string]Candidate{}
	keep := func(word, detail string, score int) {
		if word == prefix || len(word) <= len(prefix) || word[:len(prefix)] != prefix {
			return
		}
		if old, ok := best[word]; ok && old.score >= score {
			return
		}
		best[word] = Candidate{Word: word, Detail: detail, score: score}
	}

	for _, s := range snaps {
		score := scoreSymbol
		if s.Current {
			score += scoreLocal
		}
		for _, w := range s.Symbols {
			keep(w, short(s.Path), score)
		}
	}
	for _, s := range snaps {
		score, detail := scoreOther, short(s.Path)
		if s.Current {
			score, detail = scoreLocal, ""
		}
		for _, w := range c.Words(s.Path, s.Version, s.Text) {
			keep(w, detail, score)
		}
	}
	return rankOut(best)
}
