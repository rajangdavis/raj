package editor

import "raj/internal/view"

// Fold is one reader-placed fold: the session lines a language server offered
// to collapse, the session byte range those lines occupy, and whether the user
// has closed it. StartLine and EndLine are 0-based and inclusive and name the
// server's whole range including the header line; Lo and Hi cover the body a
// closed fold hides — StartLine+1 through EndLine — so it maps onto whole
// display rows.
type Fold struct {
	StartLine int
	EndLine   int
	Lo, Hi    int
	Closed    bool
}

// SetFolds installs the ranges a server answered. It preserves which ranges
// were closed across an answer by line range, so a fold the user made does not
// spring open on the next idle tick; an answer that moves the lines is a best
// effort, and a range that no longer appears simply drops. The version is
// recorded rather than checked here: hiddenFoldRanges refuses a list measured
// on another version, so an answer that lands after an edit cannot hide the
// wrong bytes even before the next request replaces it.
func (p *Pane) SetFolds(folds []Fold, version int) {
	next := make([]Fold, len(folds))
	copy(next, folds)
	for i := range next {
		for _, old := range p.folds {
			if old.StartLine == next[i].StartLine && old.EndLine == next[i].EndLine {
				next[i].Closed = old.Closed
				break
			}
		}
	}
	p.folds = next
	p.foldVersion = version
	p.foldRev++
}

// ClearFolds forgets every reader fold. The display memo sees a new revision
// and rebuilds without them.
func (p *Pane) ClearFolds() {
	if len(p.folds) == 0 && p.foldVersion == 0 {
		return
	}
	p.folds = nil
	p.foldVersion = 0
	p.foldRev++
}

// FoldCount is how many reader folds the pane holds. It exists so a caller can
// tell that an answer landed without reaching into the list.
func (p *Pane) FoldCount() int { return len(p.folds) }

// hiddenFoldRanges is the closed folds as the display-only ranges view.Build
// wants, or nil when they were measured on another version: an edit moves the
// bytes a list was computed against, so the folds unfold until the next answer
// rather than hiding the wrong lines.
func (p *Pane) hiddenFoldRanges() []view.FoldRange {
	if len(p.folds) == 0 || p.foldVersion != int(p.File.Session().Version()) {
		return nil
	}
	var out []view.FoldRange
	for _, f := range p.folds {
		if f.Closed && f.Hi > f.Lo {
			out = append(out, view.FoldRange{Lo: f.Lo, Hi: f.Hi})
		}
	}
	return out
}

// ToggleFoldAt flips the closed state of the innermost fold whose range
// contains the 0-based session line, and reports it. "Contains" is the header
// line or any body line, so the caret on the header closes the fold and a click
// on the fold row — which lands at the hidden run's start, inside the body —
// opens it again.
func (p *Pane) ToggleFoldAt(line int) (Fold, bool) {
	best := -1
	for i, f := range p.folds {
		if line < f.StartLine || line > f.EndLine {
			continue
		}
		if best < 0 || innermost(p.folds, i, best) {
			best = i
		}
	}
	if best < 0 {
		return Fold{}, false
	}
	p.folds[best].Closed = !p.folds[best].Closed
	p.foldRev++
	return p.folds[best], true
}

// innermost reports whether fold a is a tighter fit for a line than b: the
// smaller body wins, and on a tie the later start (the nested fold inside).
func innermost(folds []Fold, a, b int) bool {
	as, bs := folds[a].EndLine-folds[a].StartLine, folds[b].EndLine-folds[b].StartLine
	if as != bs {
		return as < bs
	}
	return folds[a].StartLine > folds[b].StartLine
}
