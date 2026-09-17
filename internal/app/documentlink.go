package app

import (
	"context"
	"time"

	"raj/internal/lsp"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Following a document link. The server names the spans of a document that
// point somewhere else and the URI to open for each; the chord asks for the
// links, the position the chord was pressed at decides which one, and a file:
// target opens through the same OpenFile path every other jump uses.
//
// A link is not rendered. The inlay-hint lane draws text before a line and the
// code-lens lane draws it at offset zero; neither can underline an arbitrary
// span, and the column map has no styling lane to borrow. Half-drawing a link
// — a hint that does not follow the text, or an underline that shifts the caret
// — is worse than leaving it a follow-command, so that is what it stays.

// documentLinkGap names why a live server cannot serve document links, or ""
// when it can. It is the one reading rule capabilityGap applies to the raw
// provider, named here so a test can assert the feature reads
// DocumentLinkProvider without a live server.
func documentLinkGap(caps lsp.ServerCapabilities) string {
	return capabilityGap(caps.DocumentLinkProvider, "document links")
}

// followLink follows the document link at the caret.
//
// The request finds or starts the server — an explicit chord is a request for a
// language feature, the same as hover — checks the capability, syncs the
// buffer, and parks the answer with the version and position it was measured
// on. The event thread follows the link only if the buffer still holds that
// version.
func (a *App) followLink() {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	path := a.docPath(p)
	ls, st := a.servers.for_(path, func() { a.host.Post(ui.Wake{}) })
	if ls == nil {
		a.status = st.message(path)
		return
	}
	if msg := documentLinkGap(ls.caps.Capabilities); msg != "" {
		a.status = msg
		return
	}
	if !a.syncDoc(ls, p) {
		return
	}
	a.lspGen++
	gen := a.lspGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	version := int(p.File.Session().Version())
	resolve := ls.caps.Capabilities.DocumentLinkResolve()
	conn := ls.srv.Conn()

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		links, err := lsp.RequestDocumentLinks(ctx, conn, path)
		if err != nil {
			return
		}
		// Resolve only the link the chord is on. Resolving every link to follow
		// one spends a round trip per link the user never asked about, and a
		// resolve-only server pays that on every follow; only the link about to
		// be opened needs its target. The list is reduced to that link so the
		// install's own position lookup selects exactly it, and a resolve that
		// returned no target is still parked, so openLink refuses it by name
		// rather than following nothing.
		if link, ok := lsp.LinkAt(links, pos); ok && resolve && link.NeedsResolve() && len(link.Data) > 0 {
			if resolved, rerr := lsp.ResolveDocumentLink(ctx, conn, link); rerr == nil {
				link = resolved
			}
			links = []lsp.DocumentLink{link}
		}
		a.park(lspAnswer{
			gen: gen, kind: answerDocumentLink, path: path,
			docVersion: version, line: pos.Line, col: pos.Character, links: links,
		})
	})
}

// applyDocumentLink follows the link the request was about.
//
// Three things must hold: the request has not been superseded, the pane still
// shows the document that asked, and the text is still the version the server
// measured — a link's range is offsets into that text, so an edit under it
// moves the link rather than the offsets. The link is chosen by the position
// the chord was pressed at rather than the caret's position now, so a caret
// that drifted while the server answered still follows what was asked for.
func (a *App) applyDocumentLink(ans lspAnswer) {
	p := a.Tabs.Active()
	if p == nil || a.docPath(p) != ans.path {
		return
	}
	if int(p.File.Session().Version()) != ans.docVersion {
		a.status = "the buffer changed before the link could be followed"
		return
	}
	link, ok := lsp.LinkAt(ans.links, lsp.Position{Line: ans.line, Character: ans.col})
	if !ok {
		a.status = "no link at the caret"
		return
	}
	a.openLink(link)
}

// openLink opens a followed link, or names why it cannot.
//
// Only a file: target opens. raj is a terminal editor with no browser and no
// handler for an arbitrary URI, so an http, https or mailto link is refused out
// loud rather than silently doing nothing — the user asked to follow it and
// deserves to hear that this editor cannot. A link the server left unresolved
// is refused by name too: the target is missing, not the file.
func (a *App) openLink(link lsp.DocumentLink) {
	if link.Target == "" {
		a.status = "this link has no target; the server did not resolve it"
		return
	}
	scheme := lsp.LinkScheme(link.Target)
	if scheme != "file" {
		if scheme == "" {
			a.status = "cannot open " + link.Target + ": raj follows file: links only"
			return
		}
		a.status = "cannot open " + scheme + ": link: raj is a terminal editor with no browser"
		return
	}
	path := lsp.Path(link.Target)
	if path == "" {
		a.status = "cannot open the link: the server named an empty file path"
		return
	}
	a.OpenFile(path)
}
