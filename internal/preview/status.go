package preview

import "time"

// The statuses a renderer reports.
const (
	StatusReady       = "ready"
	StatusProgressing = "progressing"
	StatusDegraded    = "degraded"
	StatusSyncFailed  = "sync-failed"
)

// ValidStatus reports whether s is one of the statuses a renderer reports.
func ValidStatus(s string) bool {
	switch s {
	case StatusReady, StatusProgressing, StatusDegraded, StatusSyncFailed:
		return true
	}
	return false
}

// ServiceStatus is the latest status of one service of a preview: the
// revision it is about, the status and when the renderer reported it.
type ServiceStatus struct {
	Revision string    `json:"revision"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}
