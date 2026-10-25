package formula

import (
	"fmt"
	"math"
	"time"

	"github.com/expr-lang/expr"

	"github.com/anyproto/anytype-heart/core/domain"
)

// Source gives a formula the values it reads. Values use the Go types formulas
// see: float64 for numbers, string for text and status, bool for checkboxes,
// time.Time for dates and []string for tags, files and object relations.
// A missing value is nil.
type Source interface {
	// Value returns a property of the object the formula is evaluated on.
	Value(key domain.RelationKey) any
	// Related returns a property of every object linked through rel, in link order.
	Related(rel, key domain.RelationKey) []any
	// Children returns a property of every object whose rel relation links to the object.
	Children(rel, key domain.RelationKey) []any
}

// Evaluate runs the formula for one object and returns its value as a detail value.
func (f *Formula) Evaluate(src Source) (domain.Value, error) {
	env := make(map[string]any, len(f.env))
	for _, dep := range f.deps {
		name := identifier(dep)
		zero := f.env[name]
		if dep.Rel == "" {
			env[name] = valueOrZero(src.Value(dep.Key), zero)
			continue
		}
		values := src.Related
		if dep.Reverse {
			values = src.Children
		}
		list, err := toList(values(dep.Rel, dep.Key), zero)
		if err != nil {
			return domain.Value{}, fmt.Errorf("read related %s.%s: %w", dep.Rel, dep.Key, err)
		}
		env[name] = list
	}

	out, err := expr.Run(f.program, env)
	if err != nil {
		return domain.Value{}, &Error{Message: err.Error()}
	}
	return toDetailValue(out)
}

func valueOrZero(v, zero any) any {
	if v == nil {
		return zero
	}
	return v
}

// toList converts related values to the typed list the formula was compiled with.
// Empty values are skipped, and list values (tags, objects) are flattened.
func toList(values []any, zero any) (any, error) {
	switch zero.(type) {
	case []float64:
		out := make([]float64, 0, len(values))
		for _, v := range values {
			if v == nil {
				continue
			}
			n, ok := v.(float64)
			if !ok {
				return nil, fmt.Errorf("expected a number, got %T", v)
			}
			out = append(out, n)
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(values))
		for _, v := range values {
			switch s := v.(type) {
			case nil:
			case string:
				out = append(out, s)
			case []string:
				out = append(out, s...)
			default:
				return nil, fmt.Errorf("expected text, got %T", v)
			}
		}
		return out, nil
	case []bool:
		out := make([]bool, 0, len(values))
		for _, v := range values {
			if v == nil {
				out = append(out, false)
				continue
			}
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("expected a checkbox, got %T", v)
			}
			out = append(out, b)
		}
		return out, nil
	case []time.Time:
		out := make([]time.Time, 0, len(values))
		for _, v := range values {
			if v == nil {
				continue
			}
			t, ok := v.(time.Time)
			if !ok {
				return nil, fmt.Errorf("expected a date, got %T", v)
			}
			out = append(out, t)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported list type %T", zero)
}

func toDetailValue(out any) (domain.Value, error) {
	switch v := out.(type) {
	case float64:
		return number(v)
	case int:
		return number(float64(v))
	case int64:
		return number(float64(v))
	case string:
		return domain.String(v), nil
	case bool:
		return domain.Bool(v), nil
	case time.Time:
		return domain.Int64(v.Unix()), nil
	}
	return domain.Value{}, &Error{Message: fmt.Sprintf("unsupported result %T", out)}
}

func number(v float64) (domain.Value, error) {
	if math.IsInf(v, 0) {
		return domain.Value{}, &Error{Message: "division by zero"}
	}
	if math.IsNaN(v) {
		return domain.Value{}, &Error{Message: "result is not a number"}
	}
	return domain.Float64(v), nil
}
