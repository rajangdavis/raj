package hooks

import (
	"sort"
	"sync"
	"time"
)

// Run is one in-flight hook run.
type Run struct {
	ID     uint64 `json:"id"`
	Hook   string `json:"hook"`
	Author uint8  `json:"author"`
	PID    int    `json:"pid"`
	PGID   int    `json:"pgid"`
	// Started is when the run was registered.
	Started time.Time `json:"started"`
	// Cancel stops the run by cancelling its context, which kills the process
	// group the command leads. It is nil for a run a caller registered without
	// offering cancellation.
	Cancel func() `json:"-"`
}

// Registry tracks in-flight runs so a shutdown or a caller can list and kill
// them. A run executes off the event thread, so it is safe for concurrent use.
type Registry struct {
	mu   sync.Mutex
	next uint64
	runs map[uint64]Run
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{runs: make(map[uint64]Run)}
}

// Add stores run, assigning it the next ID, and returns that ID. Any ID the
// caller set is replaced: the registry is the one authority for IDs.
func (r *Registry) Add(run Run) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	run.ID = r.next
	r.runs[run.ID] = run
	return run.ID
}

// Remove forgets the run with id. Removing an absent id is a no-op.
func (r *Registry) Remove(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, id)
}

// Get returns the run with id and whether it is present.
func (r *Registry) Get(id uint64) (Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	return run, ok
}

// SetProcess records the started command's pid and process-group id on the
// run with id, so `hook ps` can report them and `hook cancel` can name the
// process it stopped. Setting them on a run that has already gone is a no-op.
func (r *Registry) SetProcess(id uint64, pid, pgid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return
	}
	run.PID, run.PGID = pid, pgid
	r.runs[id] = run
}

// SetCancel records cancel on the run with id, for a caller that can only
// build the killer after the process has started. Setting it on a run that has
// already gone is a no-op.
func (r *Registry) SetCancel(id uint64, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return
	}
	run.Cancel = cancel
	r.runs[id] = run
}

// Adopt records run under an explicit id, for a run a previous editor started
// that this one re-attaches so its file name, `hook ps` row and cancel target
// all keep the id the run was given. It raises next above id.
func (r *Registry) Adopt(id uint64, run Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run.ID = id
	r.runs[id] = run
	if id > r.next {
		r.next = id
	}
}

// Seed raises the next id to at least min, so ids assigned after a restart do
// not reuse the file names of runs recorded before it. It never lowers the
// next id.
func (r *Registry) Seed(min uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if min > r.next {
		r.next = min
	}
}

// List returns a snapshot of the in-flight runs, stable by ID. It is never
// nil.
func (r *Registry) List() []Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Run, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len returns the number of in-flight runs.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}
