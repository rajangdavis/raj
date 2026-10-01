package intent

import (
	"context"
	"strings"

	"raj/internal/git"
)

// Objects is one export's object ids: the tree, the commit, the commit it
// parented on, and the base it was materialised over. An export writes these
// objects and nothing else — no note, no ref, no index or worktree move.
type Objects struct {
	Tree    string
	Commit  string
	Parent  string
	BaseSHA string
}

// Message is an artifact commit message: a title line and an optional body. It
// is not part of an intention identity - the caller supplies it at export time,
// so the same tree can be committed under a different message, and the socket
// carries the caller text until the seam pane prompts for it.
type Message struct {
	Title string
	Body  string
}

// Text renders the commit message. An empty title falls back to def, so an
// export with no caller message keeps its name-derived default; a body is
// appended after a blank line. Surrounding whitespace is dropped, so a flag
// value that carries a trailing newline does not leave a blank subject.
func (m Message) Text(def string) string {
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = def
	}
	body := strings.TrimSpace(m.Body)
	if body == "" {
		return title
	}
	return title + "\n\n" + body
}

// Export materialises in over base with proj and writes one commit parented on
// base (the base ref's head) with msg as its message. It writes objects only —
// no ref moves, HEAD is
// untouched, the worktree is untouched — and is repeatable. The commit date is
// pinned to in.Created, so the same inputs give the same commit id (D3). There
// is no note: provenance lives in the raj journal, not in git.
func Export(ctx context.Context, svc *git.Service, in Intention, base string, proj Projection, msg Message) (Objects, error) {
	tree, err := Materialise(ctx, svc, base, proj)
	if err != nil {
		return Objects{}, err
	}
	var parents []string
	if base != "" {
		parents = append(parents, base)
	}
	commit, err := svc.CommitTreeAt(ctx, tree, parents, msg.Text("intention "+in.Name), in.Created)
	if err != nil {
		return Objects{}, err
	}
	return Objects{Tree: tree, Commit: commit, Parent: base, BaseSHA: base}, nil
}
