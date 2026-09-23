// Package picker is the quick-open overlay: a floating list with a fuzzy query,
// not a sidebar.
//
// It serves both cmd+p and cmd+shift+o because they are the same widget with
// different rows. Splitting them would mean a second overlay, a second focus
// state, a second keymap scope and a second renderer, all to display a list of
// strings that is filtered as you type — and the two would drift. What differs
// is where the rows come from and what choosing one means, which is two fields.
package picker

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/hidden"
	"raj/internal/keys"
	"raj/internal/symbols"
	"raj/internal/ui"
	"raj/internal/widget"
)

// MaxFiles bounds the index. A picker that takes seconds to open in a large
// repository is worse than one that quietly stops indexing.
const MaxFiles = 20000

// Mode is what the overlay is listing.
type Mode int

const (
	// Files lists the workspace, and choosing one opens it.
	Files Mode = iota
	// Symbols lists the declarations in one file, and choosing one jumps.
	Symbols
	// Proposals lists the pending change sets in one file, and choosing one
	// jumps to where it sits.
	Proposals
	// Commands lists the actions the keymap binds, and choosing one runs it.
	Commands
	// References lists a symbol's uses across files; choosing a row opens the
	// file at the use and leaves the place where PositionFor will find it.
	References
	// CodeActions lists the actions a language server offers for the caret;
	// choosing one names its index back to the caller, which applies the edit
	// or runs the command the action carries.
	CodeActions
)

// Proposal is one row in Proposals mode: a pending change set, labelled for a
// review pass, and the 1-based line choosing it jumps to.
type Proposal struct {
	Label string
	Line  int
}

type Command struct {
	Label  string
	Action keys.Action
}

// Reference is one row in References mode: a labelled use and where it is. Path
// is absolute, because the uses of a symbol can lie outside the workspace root
// the file rows are relative to, and a list of them is not scoped to one file
// the way symbols and proposals are.
type Reference struct {
	Label string
	Path  string
	Line  int
	Col   int
}

// CodeAction is one row in CodeActions mode: the action's title and the index
// choosing it names back to the caller. The action itself stays with the caller
// because it is a language-server value, not a picker one, and the picker's job
// is only to show the labels and report which was chosen.
type CodeAction struct {
	Label string
	Index int
}

// Picker is the floating quick-open overlay.
type Picker struct {
	// Roots is the workspace root set the picker indexes, in supplied order.
	// Every row shows its path relative to the root it was found under, and
	// choosing it resolves back to that same root, so a file under the second
	// root opens as itself rather than under the first. NewRoots copies the
	// slice.
	Roots []string
	Open  bool
	// Tall draws each result row two screen rows high, for a touch surface.
	// The hit test divides by the same row height, so a tap anywhere in a row
	// chooses what is drawn there.
	Tall bool

	// Hidden is the visibility policy, the same one the tree and the search
	// use, so a file you can see in the sidebar is a file cmd+p can reach.
	Hidden *hidden.Rules

	mode  Mode
	items []entry
	shown []scored
	input widget.Input
	list  widget.List

	// pos is the line:col a paste carried, and path is the form of the query
	// it was attached to. Held rather than discarded so that pasting a
	// compiler line opens the file where the compiler was pointing. Cleared
	// by anything that changes the query, since a position that outlives the
	// path it came from would land in whatever file is chosen next.
	pos  Position
	path string

	// file is the document Symbols mode is listing, held so a chosen symbol
	// can name it.
	file string

	// command is the action the last choice named in Commands mode, or None.
	// It is how the palette hands its choice back: the picker names a command,
	// the caller dispatches it.
	command keys.Action

	// codeAction is the index the last choice named in CodeActions mode, and
	// codeChosen says whether a choice was actually made: index zero is a real
	// row, so a sentinel cannot stand in for "none". It is how the code-action
	// list hands its choice back, the same way command does for the palette.
	codeAction int
	codeChosen bool
}

// Position is a place in a file, 1-based, as a compiler or grep prints it. A
// zero Line means the paste carried no position.
type Position struct{ Line, Col int }

// entry is one row. label is what is shown and what the query is matched
// against; line is where choosing it goes and is zero for a file, and action
// is the command a Commands row runs.
type entry struct {
	label string
	// root is the workspace root label is relative to, so choosing the row
	// resolves back to the root it was found under. It is empty for the modes
	// whose rows name their own path or hold one file.
	root   string
	line   int
	col    int
	action keys.Action
	// path is the file a References row names. The other path modes hold one
	// file for every row; a reference list spans files, so the row carries its
	// own.
	path string
	// codeAction is the index a CodeActions row names. The list itself lives
	// with the caller; the row only carries its position in it.
	codeAction int
}

type scored struct {
	entry
	score int
	hits  []int // byte offsets in the label that matched, for highlighting
}

// New builds a picker rooted at one directory. It is NewRoots for a single
// root, so every existing caller and test is untouched.
func New(root string) *Picker {
	return NewRoots([]string{root})
}

// NewRoots builds a picker over a set of workspace roots. The index walks every
// root and holds each path relative to the root it was found under, so the
// quick-open list covers the whole workspace rather than the primary root
// alone. The hidden policy is one config per root set (hidden.WorkspaceFile),
// loaded from the whole set rather than a single root.
func NewRoots(roots []string) *Picker {
	p := &Picker{Roots: append([]string(nil), roots...), Hidden: hidden.Load(roots)}
	p.input = widget.Input{Label: "Go to file"}
	return p
}

// primaryRoot is the first root in supplied order, or "" when there is none. It
// is the root the hidden policy is loaded from, matching the single-root picker.
func primaryRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

// Show opens the file finder, reindexing so files created since last time
// appear.
func (p *Picker) Show() {
	p.reset(Files, "Go to file")
	p.index()
	p.filter()
}

// ShowSymbols opens the same overlay over one file's declarations. The path is
// held so that choosing a symbol names the file it came from, which is what
// lets a caller open and jump with the machinery it already has for a file.
func (p *Picker) ShowSymbols(path string, syms []symbols.Symbol) {
	p.reset(Symbols, "Go to symbol")
	p.file = path
	p.items = p.items[:0]
	for _, s := range syms {
		label := s.Name
		if s.Kind != "" {
			label += "  " + string(s.Kind)
		}
		p.items = append(p.items, entry{label: label, line: s.Line})
	}
	p.filter()
}

// ShowProposals opens the same overlay over the pending change sets of one
// file, for the review pass: the rows are already labelled by the caller,
// choosing one jumps to the line the change sits on, and accept and reject
// are the chords that decide it there.
func (p *Picker) ShowProposals(path string, rows []Proposal) {
	p.reset(Proposals, "Review change")
	p.file = path
	p.items = p.items[:0]
	for _, r := range rows {
		p.items = append(p.items, entry{label: r.Label, line: r.Line})
	}
	p.filter()
}

func (p *Picker) ShowCommands(rows []Command) {
	p.reset(Commands, "Run command")
	p.items = p.items[:0]
	for _, r := range rows {
		p.items = append(p.items, entry{label: r.Label, action: r.Action})
	}
	p.filter()
}

// ShowReferences opens the same overlay over a symbol's uses. Unlike symbols and
// proposals, which hold one file for every row, each row carries its own path
// and position: a reference list crosses files. Choosing a row hands back that
// path with the place in it, which is what openFromPicker already consumes.
func (p *Picker) ShowReferences(rows []Reference) {
	p.ShowLocations(rows, "References")
}

// ShowLocations opens the same overlay under a caller-chosen heading. It is
// ShowReferences generalised: an implementation list has the same shape — a
// label, a path and a place per row, crossing files — and differs only in what
// the list is called. The reference path keeps its name so the common case
// still reads the way it did.
func (p *Picker) ShowLocations(rows []Reference, title string) {
	p.reset(References, title)
	p.items = p.items[:0]
	for _, r := range rows {
		p.items = append(p.items, entry{label: r.Label, line: r.Line, col: r.Col, path: r.Path})
	}
	p.filter()
}

// ShowCodeActions opens the same overlay over the actions a language server
// offers for the caret. A row is a label plus an index, so choosing one names
// the index back to the caller rather than opening a file; the action itself
// stays with the caller, which owns the server connection and the edit path.
func (p *Picker) ShowCodeActions(rows []CodeAction, title string) {
	p.reset(CodeActions, title)
	p.items = p.items[:0]
	for _, r := range rows {
		p.items = append(p.items, entry{label: r.Label, codeAction: r.Index})
	}
	p.filter()
}

func (p *Picker) reset(m Mode, label string) {
	p.Open = true
	p.mode = m
	p.file = ""
	p.command = keys.None
	p.codeAction, p.codeChosen = 0, false
	p.pos, p.path = Position{}, ""
	p.input.Label = label
	p.input.SetText("")
	p.input.Focused = true
}

// Mode is what the overlay is currently listing.
func (p *Picker) Mode() Mode          { return p.mode }
func (p *Picker) Action() keys.Action { return p.command }

// ChosenCodeAction is the index the last choice named in CodeActions mode, and
// whether a choice was made. The boolean is the "none" signal because index
// zero is a real row. It is how the code-action list hands its choice back,
// the same way Action does for the palette.
func (p *Picker) ChosenCodeAction() (int, bool) {
	return p.codeAction, p.codeChosen
}

// ActiveInput is the query field while the overlay is open.
func (p *Picker) ActiveInput() *widget.Input {
	if !p.Open {
		return nil
	}
	return &p.input
}

// Hide closes it.
func (p *Picker) Hide() { p.Open = false }

// Resolve turns an indexed path into one a caller can open.
//
// The index holds each path relative to the root it was found under because
// that is what the list shows and what the fuzzy score ranks: an absolute
// prefix is the same bytes on every entry, so it only dilutes the score and
// eats the width. But a relative path handed to a caller is resolved against
// the process working directory, not against the workspace — so
// `raj ~/code/thing` run from anywhere else opened a blank buffer named after
// the file that was picked, silently, and saving it would have written a new
// file next to wherever the shell happened to be. The two representations both
// have to exist; the seam between them is here.
//
// A label that is in the index resolves against the root it was found under;
// one that is not (a symbol path, or a caller's own label) falls back to the
// primary root, which is the single-root answer unchanged.
func (p *Picker) Resolve(rel string) string {
	if rel == "" || filepath.IsAbs(rel) {
		return rel
	}
	for _, it := range p.items {
		if it.label == rel && it.root != "" {
			return filepath.Join(it.root, rel)
		}
	}
	if root := primaryRoot(p.Roots); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

// rowHeight is how many screen rows one result occupies. The renderer and
// ClickAt both read it, so the hit test cannot disagree with the drawing.
func (p *Picker) rowHeight() int {
	if p.Tall {
		return 2
	}
	return 1
}

// Scroll moves the visible window without moving the selection, for the wheel.
func (p *Picker) Scroll(delta int) { p.list.Scroll(delta, len(p.shown)) }

// Query is the current filter text.
func (p *Picker) Query() string { return p.input.Text }

// Results is how many files match the query.
func (p *Picker) Results() int { return len(p.shown) }

// Files returns the indexed paths, relative to their own root, in walk order. It is
// the index itself rather than the filtered rows, so a caller can ask what the
// picker can reach without going through a query, across every root.
func (p *Picker) Files() []string {
	out := make([]string, 0, len(p.items))
	for _, it := range p.items {
		out = append(out, it.label)
	}
	return out
}

// Top is the highest-ranked match, or "" when nothing matches.
func (p *Picker) Top() string {
	if len(p.shown) == 0 {
		return ""
	}
	return p.shown[0].label
}

// Handle applies an action, returning a chosen path.
func (p *Picker) Handle(a keys.Action, text string) (path string) {
	switch a {
	case keys.Cancel:
		p.Hide()
		return ""
	case keys.LineUp:
		p.list.Move(-1, len(p.shown))
		return ""
	case keys.LineDown:
		p.list.Move(+1, len(p.shown))
		return ""
	case keys.Confirm:
		if p.list.Sel < len(p.shown) {
			return p.choose(p.shown[p.list.Sel])
		}
		return ""
	}
	if p.input.Handle(a, text) {
		p.filter()
	}
	return ""
}

// Paste sets the query from a pasted payload rather than inserting it verbatim.
//
// A paste into the picker is nearly always a path from somewhere else — a
// shell, a stack trace, a grep line — and those carry decoration the index does
// not: an absolute prefix, a `:line:col` suffix, a leading `./`. The fuzzy match
// is a subsequence test, so a single stray byte that appears nowhere in the
// path drops the result count to zero. The field held what was pasted and the
// list showed nothing, which reads as the paste having been ignored.
//
// So a paste is treated as a hint and narrowed until it matches: the payload as
// pasted, then with its position suffix removed, then relative to the root, then
// the base name alone. The first form with any match wins. When none match the
// original is restored, because a picker that shows nothing for what you pasted
// is at least honest about what it searched.
func (p *Picker) Paste(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	p.input.SetText(strings.TrimSpace(text))
	p.filter()
}

// PositionFor reports the place the query asked for, if the chosen path is the
// one it named.
//
// The check matters because narrowing can widen the query: "app.go:464" pasted
// in a tree with several app.go files searches for "app.go", and arrowing to a
// different one of them must not inherit 464. A suffix match is the rule —
// exact when the paste was already relative, and base-name when narrowing went
// that far.
func (p *Picker) PositionFor(chosen string) (Position, bool) {
	if p.pos.Line == 0 && p.pos.Col == 0 {
		return Position{}, false
	}
	if chosen != p.path && !strings.HasSuffix(chosen, string(filepath.Separator)+p.path) {
		return Position{}, false
	}
	return p.pos, true
}

// pasteCandidates lists the forms of a pasted payload to try, most specific
// first, without duplicates.
func pasteCandidates(text string, roots []string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	out := []string{text}
	add := func(s string) {
		if s == "" || s == "." {
			return
		}
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}

	bare, _, _ := splitPosition(text)
	add(bare)
	bare = strings.TrimPrefix(bare, "./")
	add(bare)
	if filepath.IsAbs(bare) {
		// Roots are not nested, so at most one contains the path. Try each in
		// supplied order and stop at the first that does.
		for _, root := range roots {
			if root == "" {
				continue
			}
			if rel, err := filepath.Rel(root, bare); err == nil && !strings.HasPrefix(rel, "..") {
				add(rel)
				break
			}
		}
	}
	add(filepath.Base(bare))
	return out
}

// splitPosition separates a trailing :line or :line:col from a path, which is
// how a compiler, a linter and `grep -n` all name a place in a file. A colon is
// legal in a file name, so only an all-digit tail is taken, and at most two
// segments of one.
//
// It reads right to left, so the column is found before the line. A single
// trailing number is a line, not a column: "app.go:464" is what every tool
// prints when it has only one to give.
func splitPosition(s string) (path string, line, col int) {
	for i := 0; i < 2; i++ {
		j := strings.LastIndexByte(s, ':')
		if j <= 0 || j == len(s)-1 || !allDigits(s[j+1:]) {
			break
		}
		n, ok := atoi(s[j+1:])
		if !ok {
			break
		}
		line, col = n, line
		s = s[:j]
	}
	return s, line, col
}

// atoi accepts a non-negative decimal and bounds it, so a pasted line number of
// forty digits cannot overflow into a negative offset.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30, true
		}
	}
	return n, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// choose closes the overlay and returns the path the choice names, or "" for a
// command. A symbol answers with the file it lives in and leaves its line where
// PositionFor will find it, so the path modes come out of Handle as "a path,
// possibly with a place in it" and the caller needs one path rather than two. A
// Commands row instead names its action in p.command.
func (p *Picker) choose(s scored) string {
	p.Hide()
	if p.mode == Commands {
		// A command names an action rather than a place: the caller runs it
		// through the dispatch a chord takes.
		p.command = s.action
		return ""
	}
	if p.mode == CodeActions {
		// A code action is the caller's to carry out: it names an index, not a
		// place and not a keymap action.
		p.codeAction, p.codeChosen = s.codeAction, true
		return ""
	}
	if p.mode == References {
		if s.path == "" {
			return ""
		}
		p.pos, p.path = Position{Line: s.line, Col: s.col}, s.path
		return s.path
	}
	if p.mode == Symbols || p.mode == Proposals {
		if p.file == "" {
			return ""
		}
		p.pos, p.path = Position{Line: s.line}, p.file
		return p.file
	}
	if s.root != "" {
		return filepath.Join(s.root, s.label)
	}
	return p.Resolve(s.label)
}

func (p *Picker) index() {
	p.items = p.items[:0]
	for _, root := range p.Roots {
		if len(p.items) >= MaxFiles {
			break
		}
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && p.Hidden.HiddenPath(root, path, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if p.Hidden.HiddenPath(root, path, false) {
				return nil
			}
			if len(p.items) >= MaxFiles {
				return filepath.SkipAll
			}
			rel, err := filepath.Rel(root, path)
			if err == nil {
				p.items = append(p.items, entry{label: rel, root: root})
			}
			return nil
		})
	}
}

// filter rescores every row against the query. An empty query lists everything,
// so cmd+p with no typing is a plain file list and cmd+shift+o with no typing is
// the file's outline in file order.
//
// The sort is stable and only runs for a non-empty query, which is what keeps
// that outline in file order: ties would otherwise be reordered by whatever the
// sort felt like, and a symbol list shuffled against the file it describes is
// worse than no list.
func (p *Picker) filter() {
	q := p.input.Text
	p.pos, p.path = Position{}, ""

	// The query as typed always wins if it matches anything at all.
	if p.score(q); len(p.shown) > 0 || q == "" {
		p.list.Reset()
		return
	}

	// Nothing matched, so treat the query as a path from somewhere else and
	// narrow it: without its position suffix, then relative to the root, then
	// the base name alone. The first form with a match wins.
	//
	// This runs on every query rather than only on a paste, because the editor
	// cannot tell the two apart. A terminal that does not honour bracketed
	// paste delivers a pasted path as individual keystrokes, and then the
	// query is a path with a `:464:12` on the end that no file contains — the
	// field is full and the list is empty, which reads as the paste having
	// been ignored. Narrowing here works whichever way the bytes arrived.
	_, line, col := splitPosition(q)
	for _, cand := range pasteCandidates(q, p.Roots) {
		if cand == q {
			continue // already tried, and it is what we fall back to
		}
		if p.score(cand); len(p.shown) > 0 {
			p.pos, p.path = Position{line, col}, cand
			p.list.Reset()
			return
		}
	}

	// Nothing matched in any form. Show the query as given: a list that is
	// empty for what was actually typed is at least honest about what it
	// searched for.
	p.score(q)
	p.list.Reset()
}

// score fills shown with everything matching q, ranked. An empty query lists
// everything, so cmd+p with no typing is a plain file list and cmd+shift+o with
// no typing is the file's outline in file order.
//
// The sort is stable and only runs for a non-empty query, which is what keeps
// that outline in file order: ties would otherwise be reordered arbitrarily,
// and a symbol list shuffled against the file it describes is worse than none.
func (p *Picker) score(query string) {
	p.shown = p.shown[:0]
	q := strings.ToLower(query)
	for _, it := range p.items {
		if q == "" {
			p.shown = append(p.shown, scored{entry: it})
			continue
		}
		if p.mode == Proposals {
			// Proposal labels are prose ("group 4 · AgentD · -7 bytes · 1 op(s)"),
			// not paths: a subsequence match lets a group number collide with the
			// digits in the byte delta, which is every label in production. Tokens
			// are what a reviewer types — a group number, an author name.
			if s, hits, ok := tokenMatch(it.label, q); ok {
				p.shown = append(p.shown, scored{it, s, hits})
			}
			continue
		}
		if s, hits, ok := fuzzy(it.label, q); ok {
			p.shown = append(p.shown, scored{it, s, hits})
		}
	}
	if q != "" {
		sort.SliceStable(p.shown, func(i, j int) bool { return p.shown[i].score > p.shown[j].score })
	}
}

// fuzzy scores a query against a path and reports which bytes matched.
//
// Scattered subsequence matches are the trap: "test" is a subsequence of
// in-t-ernal/ui/styl-e.go -> s -> t, so a naive per-character score ranks it
// alongside a file actually named test.go. The fix is to make contiguity
// dominate — a run of n adjacent characters scores quadratically, gaps cost,
// and an exact substring of the base name outweighs anything spread across
// directories.
//
// The match is anchored to the file name whenever the query fits inside it,
// and falls back to the whole path otherwise. Without that anchor the scan is
// greedy from the left and spends query characters on directories: typing
// buffer_test.go put internal/app/search_buffer_test.go above the file
// actually called buffer_test.go, because the b of the query was consumed by
// the b in "piecetable" and split the real name into a run of 1 and a run of
// 13, while the other path had no b before "buffer" and matched as one clean
// run of 14. Contiguity is quadratic and unbounded, so a longer accidental run
// outscored an exact name — and typing a file's own name is the single most
// common thing anyone does here.
//
// Anchoring makes that comparison fair rather than adding another bonus to
// outweigh it: both candidates are then scored over their names, both as one
// run, and the tie is broken by the prefix and equality bonuses that were
// always meant to decide it. A query containing a separator, or one that is
// not in the name at all, still matches across the path — that is what makes
// "app/app" or a fragment of a directory work.
func fuzzy(path, query string) (score int, hits []int, ok bool) {
	lower := strings.ToLower(path)
	baseAt := strings.LastIndexByte(path, filepath.Separator) + 1
	base := lower[baseAt:]

	// from is where scoring starts: the name when the query fits there.
	from := 0
	if subsequence(base, query) {
		from = baseAt
	}

	qi, run, gaps := 0, 0, 0
	for i := from; i < len(lower) && qi < len(query); i++ {
		if lower[i] != query[qi] {
			if qi > 0 {
				gaps++
			}
			run = 0
			continue
		}
		hits = append(hits, i)
		run++
		score += run * run * 3 // contiguity dominates
		if i >= baseAt {
			score += 8 // in the file name rather than a directory
		}
		if i == baseAt || i == 0 || lower[i-1] == filepath.Separator ||
			lower[i-1] == '_' || lower[i-1] == '-' || lower[i-1] == '.' {
			score += 12 // at a word boundary
		}
		qi++
	}
	if qi < len(query) {
		return 0, nil, false
	}

	switch {
	case base == query:
		// The name is exactly what was typed. Nothing else in the tree is a
		// better answer, and no accumulation of contiguity should be able to
		// claim otherwise.
		score += 600
	case strings.HasPrefix(base, query):
		score += 300
	case strings.Contains(base, query):
		score += 200
	case strings.Contains(lower, query):
		score += 60 // contiguous, but somewhere in the path
	}
	return score - gaps*4 - len(path)/4, hits, true
}

// subsequence reports whether every byte of query appears in s in order. It is
// the same test the scoring loop applies, run ahead of it to decide where to
// start.
func subsequence(s, query string) bool {
	if query == "" {
		return true
	}
	qi := 0
	for i := 0; i < len(s); i++ {
		if s[i] == query[qi] {
			qi++
			if qi == len(query) {
				return true
			}
		}
	}
	return false
}

// tokenMatch scores a query against a proposals label: the query must equal a
// whole token, or prefix one. Tokens are the words between spaces and middle
// dots, so "4" matches "group 4 · AgentD · -7 bytes" and matches nothing in
// "group 3 · AgentD · +40 bytes" — the byte-delta digits stay inert, which is
// the entire point: a reviewer types a group number or an author name, never
// a scattered subsequence.
//
// Exact tokens outrank prefixes, and earlier tokens outrank later ones, so
// the group id — the first word — wins any tie.
func tokenMatch(label, query string) (score int, hits []int, ok bool) {
	lower := strings.ToLower(label)
	best := -1
	at := 0
	for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == ' ' || r == '·'
	}) {
		i := strings.Index(lower[at:], tok)
		start := at + i
		at = start + len(tok)
		s := -1
		switch {
		case tok == query:
			s = 1000
		case strings.HasPrefix(tok, query):
			s = 400
		}
		if s < 0 {
			continue
		}
		s -= start // earlier tokens win ties
		if s > best {
			best, hits = s, nil
			for j := 0; j < len(query); j++ {
				hits = append(hits, start+j)
			}
			ok = true
		}
	}
	return best, hits, ok
}

// Render draws the overlay centred horizontally in the upper third, where
// VSCode puts it — close to the top so results have room, but not so high it
// looks like part of the tab bar.
// box is where the overlay sits on a screen of the given size, and whether it
// fits at all. Render and ClickAt share it so a click cannot resolve against a
// rectangle other than the one that was drawn.
func (p *Picker) box(cols, rows int) (x, y, w, h int, ok bool) {
	w = cols * 2 / 3
	if w < 30 {
		w = cols - 4
	}
	h = 15
	if h > rows-4 {
		h = rows - 4
	}
	if w < 10 || h < 5 {
		return 0, 0, 0, 0, false
	}
	return (cols - w) / 2, 2, w, h, true
}

// Rows above the list inside the overlay: the border, the three-row field.
const listTop = 1 + widget.Height

// ClickAt handles a press at a screen cell while the picker is open. It returns
// a chosen path, and reports whether the press was inside the overlay — a press
// outside it belongs to nothing, since the picker is modal and the panes behind
// it are not accepting clicks.
func (p *Picker) ClickAt(cols, rows, col, row int) (path string, inside bool) {
	if !p.Open {
		return "", false
	}
	x, y, w, h, ok := p.box(cols, rows)
	if !ok || col < x || col >= x+w || row < y || row >= y+h {
		return "", false
	}
	dx, dy := col-x, row-y
	if p.input.ClickAt(dx-2, dy-1, w-4) {
		return "", true
	}
	// The row is divided by the drawn row height so a tap anywhere in a tall
	// cell resolves to it; a press above the list is inside the picker but on
	// nothing selectable. The count line below the list is bounded by the rows
	// the list was drawn with, not by the overlay.
	rel := dy - listTop
	if rel < 0 {
		return "", true
	}
	listRow := rel / p.rowHeight()
	i := p.list.Top + listRow
	if listRow >= p.list.Rows || i >= len(p.shown) {
		return "", true
	}
	p.list.Sel = i
	return p.choose(p.shown[i]), true
}

func (p *Picker) Render(s *ui.Screen, cols, rows int, th widget.Theme) {
	if !p.Open {
		return
	}
	x, y, w, h, ok := p.box(cols, rows)
	if !ok {
		return
	}

	s.Fill(x, y, w, h, ui.DefaultStyle)
	widget.Box(s, x, y, w, h, th.BorderFocus)
	// Inset by one so the field's own border sits inside the overlay's rather
	// than doubling up on it.
	p.input.Render(s, x+2, y+1, w-4, th)

	top := y + listTop
	rh := p.rowHeight()
	p.list.Settle((h-listTop-1)/rh, len(p.shown))
	for row := 0; row < p.list.Rows; row++ {
		i := p.list.Top + row
		if i >= len(p.shown) {
			break
		}
		style := th.Focus(i == p.list.Sel, true)
		// Paths are recognisable by their tail and symbols by their head, so
		// they truncate from opposite ends.
		label := widget.Truncate(p.shown[i].label, w-4)
		if p.mode == Files {
			label = widget.TruncateLeft(p.shown[i].label, w-4)
		}
		// The whole cell is the target, so its style covers every row; the
		// label sits on the first row.
		s.Fill(x+1, top+row*rh, w-2, rh, style)
		s.SetString(x+2, top+row*rh, label, style, w-4)
	}
	noun := " files "
	if p.mode == Symbols {
		noun = " symbols "
	}
	if p.mode == Proposals {
		noun = " changes "
	}
	if p.mode == Commands {
		noun = " commands "
	}
	if p.mode == References {
		noun = " references "
	}
	if p.mode == CodeActions {
		noun = " actions "
	}
	s.SetString(x+2, y+h-1, " "+itoa(len(p.shown))+noun, th.Dim, w-4)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
