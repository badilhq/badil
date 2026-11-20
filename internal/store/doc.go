// Package store reads and writes the gitops repository: badil.yaml and the
// preview values files. It reads a git clone of the default branch and
// writes by pushing one commit per operation (GitHubStore.Apply).
package store
