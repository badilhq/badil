package preview

// included returns the names of the parts a preview whose service entries are
// services runs, when c declares needs: every pinned entry (a catalog service,
// or a kept pinned entry of a service removed from the catalog) and every
// service and dependency in the transitive needs of the pinned catalog
// services. A kept pinned entry of a removed service has no needs. Cycles are
// followed once. It returns nil when c declares no needs: every part runs.
// inCatalog holds the names of c.Services.
func (c Catalog) included(services []ServiceEntry, inCatalog map[string]bool) map[string]bool {
	if len(c.Needs) == 0 {
		return nil
	}
	in := make(map[string]bool)
	visited := make(map[string]bool)
	var queue []string
	for _, svc := range services {
		if svc.CommitSha == "" {
			continue
		}
		in[svc.Name] = true
		if inCatalog[svc.Name] && !visited[svc.Name] {
			visited[svc.Name] = true
			queue = append(queue, svc.Name)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, need := range c.Needs[name] {
			in[need] = true
			if !visited[need] {
				visited[need] = true
				queue = append(queue, need)
			}
		}
	}
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
