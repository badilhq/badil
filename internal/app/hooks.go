package app

import "github.com/badilhq/badil/internal/lifecycle"

// AppliedHook returns the hook run when the gitops store lands a push: the
// status of the previews res deleted is deleted from st, then w wakes the
// waits.
func AppliedHook(st StatusRepository, w *Wake) func(lifecycle.Result) {
	return func(res lifecycle.Result) {
		st.Delete(res.Deleted...)
		w.Notify()
	}
}

// HeadMovedHook returns the hook run when a fetch moves the gitops store's
// head: the status of the previews no longer there is collected from st, then
// w wakes the waits.
func HeadMovedHook(st StatusRepository, w *Wake) func() {
	return func() {
		st.CollectGarbage()
		w.Notify()
	}
}
