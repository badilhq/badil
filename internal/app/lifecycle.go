package app

import (
	"github.com/badilhq/badil/internal/forge"
	"github.com/badilhq/badil/internal/lifecycle"
)

// ReconcileAPI is the store, the forge, the metrics, the fan-out, the
// namespace check and the expiry store as lifecycle.ReconcilePR expects them.
func (a *App) ReconcileAPI() lifecycle.ReconcileAPI {
	return lifecycle.ReconcileAPI{
		Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut, Namespaces: a.namespaces,
		Expiry: a.expiry,
	}
}

// DeployAPI is the store, the forge, the metrics, the fan-out, the namespace
// check and the expiry store as lifecycle.DeployPR expects them.
func (a *App) DeployAPI() lifecycle.DeployAPI {
	return lifecycle.DeployAPI{
		Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut, Namespaces: a.namespaces,
		Expiry: a.expiry,
	}
}

// SyncAPI is the store, the forge, the metrics and the fan-out as
// lifecycle.Syncer expects them.
func (a *App) SyncAPI() lifecycle.SyncAPI {
	return lifecycle.SyncAPI{Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut}
}

// SweepAPI is st, f and the fan-out lifecycle.FanOutPRComments with the
// recorder m and the expiry store e, as lifecycle.Sweep expects them. A nil
// m records nothing; a nil e remembers no expiry.
func SweepAPI(
	st lifecycle.SweepStore,
	f forge.Forge,
	m lifecycle.Recorder,
	e lifecycle.ExpiryStore,
) lifecycle.SweepAPI {
	if m == nil {
		m = nopMetrics{}
	}
	return lifecycle.SweepAPI{Store: st, Forge: f, Recorder: m, FanOut: lifecycle.FanOutPRComments, Expiry: e}
}

// CatchUpAPI is st, f, the recorder m and the expiry store e, as
// lifecycle.CatchUp expects them. A nil m records nothing; a nil e skips no
// expired PR.
func CatchUpAPI(
	st lifecycle.CatchUpStore, f forge.Forge, m lifecycle.CatchUpRecorder, e lifecycle.ExpiryStore,
) lifecycle.CatchUpAPI {
	if m == nil {
		m = nopMetrics{}
	}
	return lifecycle.CatchUpAPI{Store: st, Forge: f, Recorder: m, Expiry: e}
}

// CatchUp is the catch-up of serve over the app's store, forge and metrics:
// it records the PRs it filters out as their latest outcome
// (RecordCatchUpSkip) and reconciles in the app's job queue (QueueReconcile).
func (a *App) CatchUp() *lifecycle.CatchUp {
	api := CatchUpAPI(a.store, a.forge, a.metrics, a.expiry)
	api.Skipped = a.RecordCatchUpSkip
	return lifecycle.NewCatchUp(api, a.QueueReconcile)
}
