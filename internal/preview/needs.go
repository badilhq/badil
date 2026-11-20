package preview

import "maps"

// NeedsClosure returns roots plus every service and dependency in their
// transitive needs, cycles followed once: the roots only when c declares no
// needs.
func (c Catalog) NeedsClosure(roots []string) map[string]bool {
	in := make(map[string]bool, len(roots))
	queue := make([]string, 0, len(roots))
	for _, name := range roots {
		if !in[name] {
			in[name] = true
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, need := range c.Needs[name] {
			if !in[need] {
				in[need] = true
				queue = append(queue, need)
			}
		}
	}
	return in
}

// included returns the names of the parts a preview whose service entries are
// services runs, when c declares needs: PinnedClosure of every pinned entry.
// A kept pinned entry of a service removed from the catalog has no needs. It
// returns nil when c declares no needs: every part runs.
func (c Catalog) included(services []ServiceEntry) map[string]bool {
	if len(c.Needs) == 0 {
		return nil
	}
	return c.PinnedClosure(services, func(*ServiceEntry) bool { return true })
}

// PinnedClosure returns the names of the pinned entries of services for which
// root is true (catalog services, or kept pinned entries of services removed
// from the catalog) and of every service and dependency in the transitive
// needs of the catalog services among them (NeedsClosure).
func (c Catalog) PinnedClosure(services []ServiceEntry, root func(*ServiceEntry) bool) map[string]bool {
	inCatalog := make(map[string]bool, len(c.Services))
	for _, name := range c.Services {
		inCatalog[name] = true
	}
	in := make(map[string]bool)
	var roots []string
	for i := range services {
		svc := &services[i]
		if svc.CommitSha == "" || !root(svc) {
			continue
		}
		in[svc.Name] = true
		if inCatalog[svc.Name] {
			roots = append(roots, svc.Name)
		}
	}
	maps.Copy(in, c.NeedsClosure(roots))
	return in
}

// excluded returns the catalog services then the catalog dependencies, in
// badil.yaml order, not in in, the included parts; nil when every part runs.
func (c Catalog) excluded(in map[string]bool) []string {
	var out []string
	for _, name := range c.Services {
		if !in[name] {
			out = append(out, name)
		}
	}
	for _, dep := range c.Dependencies {
		if !in[dep.Name] {
			out = append(out, dep.Name)
		}
	}
	return out
}
