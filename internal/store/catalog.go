package store

import "github.com/badilhq/badil/internal/catalog"

// ServiceCatalog is catalog.Catalog.
type ServiceCatalog = catalog.Catalog

// DefaultLabel is catalog.DefaultLabel.
const DefaultLabel = catalog.DefaultLabel

// RepoURLParser is catalog.RepoURLParser.
type RepoURLParser = catalog.RepoURLParser

// Repo is catalog.Repo.
type Repo = catalog.Repo

// ParseCatalog is catalog.Parse.
func ParseCatalog(raw []byte) (*ServiceCatalog, error) {
	return catalog.Parse(raw)
}

// RepoFullName is catalog.RepoFullName.
func RepoFullName(path string) (string, bool) {
	return catalog.RepoFullName(path)
}
