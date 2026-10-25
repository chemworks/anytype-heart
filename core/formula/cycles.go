package formula

import (
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/core/domain"
)

// CheckCycles reports an error when formulas depend on each other in a loop,
// directly or through relations. Relation keys are space-wide, so the graph is
// built over keys: a formula depends on every key it reads.
func CheckCycles(formulas map[domain.RelationKey]*Formula, schema *Schema) error {
	const (
		unvisited = iota
		inProgress
		done
	)
	state := make(map[domain.RelationKey]int, len(formulas))
	var path []domain.RelationKey

	var visit func(key domain.RelationKey) error
	visit = func(key domain.RelationKey) error {
		f, ok := formulas[key]
		if !ok {
			return nil
		}
		switch state[key] {
		case done:
			return nil
		case inProgress:
			return cycleError(append(path, key), schema)
		}
		state[key] = inProgress
		path = append(path, key)
		for _, dep := range f.deps {
			if err := visit(dep.Key); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[key] = done
		return nil
	}

	for key := range formulas {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func cycleError(path []domain.RelationKey, schema *Schema) error {
	start := 0
	last := path[len(path)-1]
	for i, k := range path {
		if k == last {
			start = i
			break
		}
	}
	names := make([]string, 0, len(path)-start)
	for _, k := range path[start:] {
		name := string(k)
		if p, ok := schema.ByKey(k); ok {
			name = p.Name
		}
		names = append(names, name)
	}
	return &Error{Message: fmt.Sprintf("formulas depend on each other: %s", strings.Join(names, " -> "))}
}
