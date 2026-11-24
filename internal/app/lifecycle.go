package app

import "github.com/badilhq/badil/internal/lifecycle"

// ReconcileAPI is the store, the forge, the metrics, the fan-out and the
// namespace check as lifecycle.ReconcilePR expects them.
func (a *App) ReconcileAPI() lifecycle.ReconcileAPI {
	return lifecycle.ReconcileAPI{
		Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut, Namespaces: a.namespaces,
	}
}

// DeployAPI is the store, the forge, the metrics, the fan-out and the
// namespace check as lifecycle.DeployPR expects them.
func (a *App) DeployAPI() lifecycle.DeployAPI {
	return lifecycle.DeployAPI{
		Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut, Namespaces: a.namespaces,
	}
}

// SyncAPI is the store, the forge, the metrics and the fan-out as
// lifecycle.Syncer expects them.
func (a *App) SyncAPI() lifecycle.SyncAPI {
	return lifecycle.SyncAPI{Store: a.store, Forge: a.forge, Recorder: a.metrics, FanOut: a.fanOut}
}
