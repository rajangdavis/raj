package git

import (
	"context"
	"fmt"
)

// NoteObject writes message as a note on annotated and returns the note blob
// and the tree that holds it. It is the deliberate lower half of Note: it
// writes git objects and moves nothing, so the export path can attach
// provenance without touching refs/notes/commits. The ref update is outward
// and belongs to publish, not to object plumbing.
//
// The tree maps annotated's full hex name to the note blob -- the shape a
// notes tree uses, without the fanout directory a large notes ref grows. A
// single-entry tree is valid and a later export can extend it. No ref is
// created or moved.
func (s *Service) NoteObject(ctx context.Context, annotated, message string) (blob, tree string, err error) {
	if annotated == "" {
		return "", "", fmt.Errorf("git: a note needs an object to annotate")
	}
	blob, err = s.HashObject(ctx, []byte(message))
	if err != nil {
		return "", "", err
	}
	tree, err = s.Mktree(ctx, []TreeEntry{{Mode: "100644", Type: "blob", Hash: blob, Name: annotated}})
	if err != nil {
		return "", "", err
	}
	return blob, tree, nil
}
