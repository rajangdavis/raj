package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"raj/internal/lsp"
	"raj/internal/picker"
	"raj/internal/safe"
	"raj/internal/ui"
)

// Sibling jump requests: declaration, type definition and implementation are
// go-to-definition's twins, and the request/sync/park pipeline is identical for
// all three. They live here rather than beside hover and definition so that the
// one shared handler is where a reader looks, and so a change to it cannot lose
// itself in the file that also holds every other language feature.
//
// pickWhenMany jumps for a single place and lists several, which is what an
// interface's implementations usually want. jumpToFirst is go-to-definition's
// shape. alwaysPick lists even a single place, which is what references wants.
type jumpMode int

const (
	jumpToFirst jumpMode = iota
	pickWhenMany
	alwaysPick
)

// locationRequest performs one of the location lookups. The signature matches
// every lsp.Request* location request, so a jump carries the function itself
// rather than a switch over a method name.
type locationRequest func(ctx context.Context, c *lsp.Conn, path string, p lsp.Position) ([]lsp.Location, error)

// jumpRequest is one lookup end to end: the word the user knows it by, the
// capability that gates it, the request to send, and what to do with the
// answer. feature is the noun in "no <feature> found" and the word the
// capability gap names.
type jumpRequest struct {
	feature string
	// provider reads the capability the request needs; nil means ungated.
	provider func(lsp.ServerCapabilities) json.RawMessage
	request  locationRequest
	mode     jumpMode
	// title is the picker's heading when the answer is listed.
	title string
}

// The three sibling jumps. They are constructors rather than literals at the
// call site so a test can assert each reads its own capability without a live
// server.
func declarationJump() jumpRequest {
	return jumpRequest{
		feature:  "declaration",
		provider: func(c lsp.ServerCapabilities) json.RawMessage { return c.DeclarationProvider },
		request:  lsp.RequestDeclaration,
	}
}

func typeDefinitionJump() jumpRequest {
	return jumpRequest{
		feature:  "type definition",
		provider: func(c lsp.ServerCapabilities) json.RawMessage { return c.TypeDefinitionProvider },
		request:  lsp.RequestTypeDefinition,
	}
}

func implementationJump() jumpRequest {
	return jumpRequest{
		feature:  "implementation",
		provider: func(c lsp.ServerCapabilities) json.RawMessage { return c.ImplementationProvider },
		request:  lsp.RequestImplementation,
		mode:     pickWhenMany,
		title:    "Implementations",
	}
}

// requestJump runs one lookup: find or start the server, check the capability,
// sync the buffer, and park the answer for the event thread. It is the single
// request/sync/generation pipeline the sibling jumps share, so a fix to one is
// a fix to all of them.
func (a *App) requestJump(r jumpRequest) {
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
	if r.provider != nil {
		if msg := capabilityGap(r.provider(ls.caps.Capabilities), r.feature); msg != "" {
			a.status = msg
			return
		}
	}
	if !a.syncDoc(ls, p) {
		return
	}

	a.lspGen++
	gen := a.lspGen
	head := p.Cursors.Primary().Head
	pos := lsp.NewDocument(p.File.Text()).Position(head)
	conn := ls.srv.Conn()

	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		locs, _ := r.request(ctx, conn, path, pos)
		a.park(lspAnswer{
			gen: gen, kind: answerJump, locs: locs,
			feature: r.feature, mode: r.mode, title: r.title,
		})
	})
}

// gotoDeclaration jumps to where the thing under the cursor is declared. It is
// often the same place as the definition; where they differ, the declaration
// is the name and the definition is the body.
func (a *App) gotoDeclaration() { a.requestJump(declarationJump()) }

// gotoTypeDefinition jumps to the definition of the thing's type: the struct
// behind a variable, the interface behind a method value.
func (a *App) gotoTypeDefinition() { a.requestJump(typeDefinitionJump()) }

// gotoImplementation lists the concrete implementations of the interface or
// interface method under the cursor. Several implementations is the normal
// case, so the answer goes to the picker when there is more than one.
func (a *App) gotoImplementation() { a.requestJump(implementationJump()) }

// applyJump routes a jump answer to the picker or the cursor. An empty answer
// names the feature, so "no implementation found" reads as the lookup it was
// rather than as a definition that failed.
func (a *App) applyJump(r lspAnswer) {
	if len(r.locs) == 0 {
		a.status = "no " + r.feature + " found"
		return
	}
	if r.mode == alwaysPick || (r.mode == pickWhenMany && len(r.locs) > 1) {
		a.showLocations(r.locs, r.title)
		return
	}
	a.jumpToLocation(r.locs[0])
}

// locationRows labels a location list for the picker: the path relative to the
// workspace where it can be, and the 1-based line and column of the place. A
// use can lie outside the workspace, so the absolute path is what is opened.
func (a *App) locationRows(locs []lsp.Location) []picker.Reference {
	rows := make([]picker.Reference, 0, len(locs))
	for _, loc := range locs {
		rel := loc.Path
		// The location can lie outside every root; fall back to the primary
		// root, which is where the single-root code spelled it.
		root := a.rootFor(loc.Path)
		if root == "" {
			root = a.primaryRoot()
		}
		if relPath, err := filepath.Rel(root, loc.Path); err == nil {
			rel = relPath
		}
		line := loc.Range.Start.Line + 1
		col := loc.Range.Start.Character + 1
		rows = append(rows, picker.Reference{
			Label: rel + ":" + itoa(line) + ":" + itoa(col),
			Path:  loc.Path,
			Line:  line,
			Col:   col,
		})
	}
	return rows
}

// showLocations lists places in the picker under a heading, so Enter opens the
// chosen one. The picker already turns a path plus a 1-based position into a
// jump through openFromPicker — the same seam a symbol choice uses — so there
// is no second listing UI to keep in step with the review and symbol pickers.
func (a *App) showLocations(locs []lsp.Location, title string) {
	a.status = ""
	a.Picker.ShowLocations(a.locationRows(locs), title)
	a.focus = FocusPicker
}
