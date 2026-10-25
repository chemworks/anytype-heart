// Package formula compiles and evaluates per-object formula properties.
//
// A formula reads properties of its own object with prop("Name") and properties
// of related objects with rel("Relation", "Name"). Names are what the user sees;
// formulas are stored with relation keys so that renaming a property does not
// break them (see ToStorage and ToDisplay).
package formula

import (
	"time"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Property describes one relation a formula may read.
type Property struct {
	Key    domain.RelationKey
	Name   string
	Format model.RelationFormat
	// ResultFormat is set for formula properties: the format their value has.
	ResultFormat model.RelationFormat
}

// Schema is the set of relations visible to formulas in a space.
type Schema struct {
	byKey  map[domain.RelationKey]Property
	byName map[string]Property
}

func NewSchema(props ...Property) *Schema {
	s := &Schema{
		byKey:  make(map[domain.RelationKey]Property, len(props)),
		byName: make(map[string]Property, len(props)),
	}
	for _, p := range props {
		s.byKey[p.Key] = p
		s.byName[p.Name] = p
	}
	return s
}

func (s *Schema) ByKey(key domain.RelationKey) (Property, bool) {
	p, ok := s.byKey[key]
	return p, ok
}

func (s *Schema) ByName(name string) (Property, bool) {
	p, ok := s.byName[name]
	return p, ok
}

// valueFormat is the format a formula sees when it reads the property.
func (p Property) valueFormat() model.RelationFormat {
	if p.Format == formatFormula {
		return p.ResultFormat
	}
	return p.Format
}

// formatFormula is the relation format of formula properties. Its value (200) is
// kept far from upstream values so that rebasing the fork does not collide.
const formatFormula = model.RelationFormat_formula

// FormatFormula is exported for callers that build schemas.
const FormatFormula = formatFormula

// zeroValue returns a value of the Go type a formula sees for the given format.
// expr uses these values to type-check formulas at compile time.
func zeroValue(format model.RelationFormat) (any, bool) {
	switch format {
	case model.RelationFormat_number:
		return float64(0), true
	case model.RelationFormat_shorttext,
		model.RelationFormat_longtext,
		model.RelationFormat_url,
		model.RelationFormat_email,
		model.RelationFormat_phone,
		model.RelationFormat_emoji,
		model.RelationFormat_status:
		return "", true
	case model.RelationFormat_checkbox:
		return false, true
	case model.RelationFormat_date:
		return time.Time{}, true
	case model.RelationFormat_tag, model.RelationFormat_object, model.RelationFormat_file:
		return []string{}, true
	default:
		return nil, false
	}
}

// zeroList returns a list of the Go type a formula sees through rel().
func zeroList(format model.RelationFormat) (any, bool) {
	switch v, ok := zeroValue(format); {
	case !ok:
		return nil, false
	default:
		switch v.(type) {
		case float64:
			return []float64{}, true
		case string:
			return []string{}, true
		case bool:
			return []bool{}, true
		case time.Time:
			return []time.Time{}, true
		case []string:
			// related lists of lists are flattened
			return []string{}, true
		}
	}
	return nil, false
}
