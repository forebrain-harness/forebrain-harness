package migrate

// progress.go derives a percentage for a running import, the number the TUI
// progress bar fills to. The event shape is uneven by nature — only the
// sessions stage carries real Done/Total counts; every other category emits
// bare Detail checkpoints — so the percent is a stage-transition state
// machine over a static slicing of the run, not an interpolation across
// stages that have nothing to interpolate:
//
//   - sessions owns [0, 90) of the bar whenever a tailored tail follows it:
//     it is the overwhelmingly long stage, and the only one whose
//     checkpoints count units, so the bar moves through it as done/total.
//     It fills the bar itself when it is the only enabled category;
//   - every other enabled category shares what is left — [90, 100) after a
//     sessions stage, the whole bar when sessions is not enabled — in
//     execution order: each category's first checkpoint snaps the bar to its
//     slice's start, and a category excluded with Only never occupies a
//     slice at all;
//   - "discovering" is pinned to 0;
//   - while the run is live the value clamps at 99 — 100% is expressed by
//     the terminal frame the surface renders when the run reports back, so
//     the bar itself never claims a finished import.
//
// The weights are a heuristic; nothing here promises an ETA. A category that
// fails midway simply never checkpoints again: the next category's first
// checkpoint moves the bar, and a run that ends right there is closed by the
// terminal frame — no stage needs an explicit "close" event.

// progressSlice is one category's share of the bar.
type progressSlice struct {
	stage  string
	lo, hi float64
}

// percentTracker is the state machine: which slices the run has, and the
// percent it last published.
type percentTracker struct {
	slices []progressSlice
	// last is the percent the tracker last published, kept so a stage
	// outside the plan holds the bar where it stands instead of snapping
	// it back to the current slice's start.
	last int
}

// newPercentTracker slices a run of the given enabled categories, in
// execution order.
func newPercentTracker(enabledInOrder []string) *percentTracker {
	const sessionsWeight = 90.0
	rest := make([]string, 0, len(enabledInOrder))
	sessions := false
	for _, stage := range enabledInOrder {
		if stage == "sessions" {
			sessions = true
			continue
		}
		rest = append(rest, stage)
	}
	tracker := &percentTracker{}
	lo := 0.0
	// The count-driven stage owns the first sessionsWeight percent of the bar
	// — but only while there is a tail to hand the remainder to. With nothing
	// after it, it fills the bar itself, so an import restricted to sessions
	// alone does not stall at the share a tail would have taken.
	if sessions {
		hi := sessionsWeight
		if len(rest) == 0 {
			hi = 100
		}
		tracker.slices = append(tracker.slices, progressSlice{stage: "sessions", lo: 0, hi: hi})
		lo = hi
	}
	if len(rest) > 0 {
		// The remaining categories share what is left — all of it when
		// sessions is absent, so an import that runs only tick-driven
		// categories travels the whole bar instead of its last tenth.
		share := (100.0 - lo) / float64(len(rest))
		for _, stage := range rest {
			hi := lo + share
			if hi > 100 {
				hi = 100
			}
			tracker.slices = append(tracker.slices, progressSlice{stage: stage, lo: lo, hi: hi})
			lo += share
		}
	}
	return tracker
}

// observe folds one checkpoint into the percent: a stage change snaps to the
// new slice's start (closing the previous slice), and the sessions stage
// interpolates inside its slice by its real counts.
func (t *percentTracker) observe(stage string, done, total int) int {
	if stage == "discovering" {
		return 0
	}
	for _, slice := range t.slices {
		if slice.stage != stage {
			continue
		}
		percent := slice.lo
		if stage == "sessions" && total > 0 {
			percent = slice.lo + (slice.hi-slice.lo)*float64(done)/float64(total)
		}
		if percent > 99 {
			percent = 99
		}
		if percent < 0 {
			percent = 0
		}
		t.last = int(percent)
		return t.last
	}
	// A stage outside the plan keeps the last known position.
	return t.last
}

// withPercentProgress wraps a surface's progress callback so every checkpoint
// carries its derived percent. A nil callback stays nil: nothing observes.
func withPercentProgress(progress func(Progress), enabledInOrder []string) func(Progress) {
	if progress == nil {
		return nil
	}
	tracker := newPercentTracker(enabledInOrder)
	return func(event Progress) {
		event.Percent = tracker.observe(event.Stage, event.Done, event.Total)
		progress(event)
	}
}

// claudeStageOrder lists the categories a Claude import runs through, in
// execution order, for the percent slicing.
func claudeStageOrder(opts *Options) []string {
	order := []string{"sessions", "memories", "skills", "plans", "mcp", "lsp", "history", "consolidating"}
	return enabledStages(opts, order)
}

// codexStageOrder lists the categories a Codex import runs through, in
// execution order, for the percent slicing.
func codexStageOrder(opts *Options) []string {
	order := []string{"sessions", "memories", "consolidating", "skills", "plans", "mcp", "history"}
	return enabledStages(opts, order)
}

// enabledStages keeps only the categories this run executes.
func enabledStages(opts *Options, order []string) []string {
	out := make([]string, 0, len(order))
	for _, stage := range order {
		if opts.wants(stage) {
			out = append(out, stage)
		}
	}
	return out
}
