package review

import (
	"encoding/json"
	"errors"
	"os"

	"raj/internal/control"
)

// Transport is the control-socket surface the review console needs: the
// workspace's pending proposals, one buffer's groups and rendered diff, and
// the three decisions. It is deliberately the whole of the console's
// conversation with the host, so the model can be exercised against a fake and
// the socket stays the only implementation.
type Transport interface {
	Proposals() ([]control.Proposal, error)
	Buffers() ([]control.Buffer, error)
	Groups(path string) ([]control.Group, error)
	Diff(path string) ([]control.DiffGroup, error)
	Accept(path string, group uint64) error
	Reject(path string, group uint64) error
	Clear(path string, group uint64) error
	Author() uint8
	Close() error
}

// socketTransport speaks the control protocol through the same Client `raj ctl`
// uses. The transport is not reimplemented here: discovery, address parsing,
// path mapping and framing all live in internal/control, and the console only
// chooses verbs.
type socketTransport struct {
	c *control.Client
}

// Dial connects the console to a running raj. addr uses the same spellings
// `raj ctl` accepts — a socket path or tcp://host:port — and empty means the
// discovered socket. Path translation is resolved here, exactly as `raj ctl`
// resolves it, so a console in a container sees local paths.
func Dial(addr string) (Transport, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	resolved, err := control.Locate(addr, cwd)
	if err != nil {
		return nil, err
	}
	c, err := control.Dial(resolved)
	if err != nil {
		return nil, err
	}
	if _, err := c.ResolveRoots(cwd); err != nil {
		c.Close()
		return nil, err
	}
	return &socketTransport{c: c}, nil
}

func (t *socketTransport) call(req control.Request) (control.Response, error) {
	res, err := t.c.Do(req)
	if err != nil {
		return res, err
	}
	if !res.OK {
		if res.Err == "" {
			res.Err = "the control address refused the request"
		}
		return res, errors.New(res.Err)
	}
	return res, nil
}

func (t *socketTransport) Proposals() ([]control.Proposal, error) {
	res, err := t.call(control.Request{Op: "proposals"})
	if err != nil {
		return nil, err
	}
	return res.Proposals, nil
}

// Buffers lists the workspace's open buffers. The console sweeps the dirty
// ones too, because a set a later edit invalidated leaves the buffer dirty but
// no longer appears in `proposals`; without this the invalid set would be
// invisible on a cold start rather than shown and undecided.
func (t *socketTransport) Buffers() ([]control.Buffer, error) {
	res, err := t.call(control.Request{Op: "buffers"})
	if err != nil {
		return nil, err
	}
	return res.Buffers, nil
}

func (t *socketTransport) Groups(path string) ([]control.Group, error) {
	res, err := t.call(control.Request{Op: "groups", Path: path})
	if err != nil {
		return nil, err
	}
	return res.Groups, nil
}

func (t *socketTransport) Diff(path string) ([]control.DiffGroup, error) {
	res, err := t.call(control.Request{Op: "diff", Path: path})
	if err != nil {
		return nil, err
	}
	if res.DiffJSON == "" {
		return nil, nil
	}
	var out []control.DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *socketTransport) Accept(path string, group uint64) error {
	_, err := t.call(control.Request{Op: "accept", Path: path, Group: group})
	return err
}

func (t *socketTransport) Reject(path string, group uint64) error {
	_, err := t.call(control.Request{Op: "reject", Path: path, Group: group})
	return err
}

// Clear disposes of a set. clear reverses text out of the buffer, so the guard
// gates it on a claim the way it gates apply; the console owns no document but
// may hold a claim for the file it is about to dispose of. Claiming the one
// path replaces the console's claim set, which is harmless because the console
// never applies text.
func (t *socketTransport) Clear(path string, group uint64) error {
	if _, err := t.call(control.Request{Op: "claim", Paths: []string{path}}); err != nil {
		return err
	}
	_, err := t.call(control.Request{Op: "clear", Path: path, Group: group})
	return err
}

func (t *socketTransport) Author() uint8 { return t.c.Author() }

func (t *socketTransport) Close() error { return t.c.Close() }
