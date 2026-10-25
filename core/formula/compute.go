package formula

import (
	"fmt"
	"sync"
	"time"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/database"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Store is the part of the space index formulas read from.
type Store interface {
	SpaceId() string
	Query(q database.Query) ([]database.Record, error)
	GetDetails(id string) (*domain.Details, error)
}

// spaceFormulas is everything compiled for one space. It is cached until a
// relation or a type of the space changes (see Invalidate).
type spaceFormulas struct {
	schema   *Schema
	formulas map[domain.RelationKey]*Formula
	errors   map[domain.RelationKey]error
	keyById  map[string]domain.RelationKey
}

var cache sync.Map // spaceId -> *spaceFormulas

// Invalidate drops the compiled formulas of a space. Call it when a relation
// changes: its name, its format or its formula.
func Invalidate(spaceId string) {
	cache.Delete(spaceId)
}

// HasFormulas reports whether the space has at least one formula property.
func HasFormulas(store Store) (bool, error) {
	sf, err := loadSpace(store)
	if err != nil {
		return false, err
	}
	return len(sf.formulas)+len(sf.errors) > 0, nil
}

func loadSpace(store Store) (*spaceFormulas, error) {
	if v, ok := cache.Load(store.SpaceId()); ok {
		return v.(*spaceFormulas), nil
	}
	sf, err := buildSpace(store)
	if err != nil {
		return nil, err
	}
	cache.Store(store.SpaceId(), sf)
	return sf, nil
}

func buildSpace(store Store) (*spaceFormulas, error) {
	records, err := store.Query(database.Query{
		Filters: []database.FilterRequest{{
			RelationKey: bundle.RelationKeyResolvedLayout,
			Condition:   model.BlockContentDataviewFilter_Equal,
			Value:       domain.Int64(model.ObjectType_relation),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("query relations: %w", err)
	}

	props := make(map[domain.RelationKey]Property, len(records))
	texts := map[domain.RelationKey]string{}
	keyById := make(map[string]domain.RelationKey, len(records))
	for _, rec := range records {
		key := domain.RelationKey(rec.Details.GetString(bundle.RelationKeyRelationKey))
		if key == "" {
			continue
		}
		format := model.RelationFormat(rec.Details.GetInt64(bundle.RelationKeyRelationFormat))
		props[key] = Property{Key: key, Name: rec.Details.GetString(bundle.RelationKeyName), Format: format}
		keyById[rec.Details.GetString(bundle.RelationKeyId)] = key
		if format == formatFormula {
			texts[key] = rec.Details.GetString(bundle.RelationKeyRelationFormula)
		}
	}

	formulas, errs := compileAll(props, texts)
	all := make([]Property, 0, len(props))
	for _, p := range props {
		all = append(all, p)
	}
	return &spaceFormulas{
		schema:   NewSchema(all...),
		formulas: formulas,
		errors:   errs,
		keyById:  keyById,
	}, nil
}

// compileAll compiles formulas in dependency order: a formula that reads another
// formula needs that formula's result format first. It fills in ResultFormat of
// the formula properties in props.
func compileAll(props map[domain.RelationKey]Property, texts map[domain.RelationKey]string) (map[domain.RelationKey]*Formula, map[domain.RelationKey]error) {
	formulas := make(map[domain.RelationKey]*Formula, len(texts))
	errs := map[domain.RelationKey]error{}
	pending := make(map[domain.RelationKey]string, len(texts))
	for k, t := range texts {
		pending[k] = t
	}

	for len(pending) > 0 {
		progress := false
		for key, text := range pending {
			if waitsForFormula(text, props, pending, key) {
				continue
			}
			f, err := Compile(text, schemaOf(props))
			delete(pending, key)
			progress = true
			if err != nil {
				errs[key] = err
				continue
			}
			formulas[key] = f
			p := props[key]
			p.ResultFormat = f.ResultFormat()
			props[key] = p
		}
		if !progress {
			// what is left reads itself or other pending formulas in a loop
			for key := range pending {
				errs[key] = &Error{Message: "formulas depend on each other"}
			}
			break
		}
	}

	if len(formulas) > 0 {
		if err := CheckCycles(formulas, schemaOf(props)); err != nil {
			for key := range formulas {
				if inCycle(key, formulas) {
					errs[key] = err
					delete(formulas, key)
				}
			}
		}
	}
	return formulas, errs
}

// waitsForFormula reports whether text reads a formula that is not compiled yet.
func waitsForFormula(text string, props map[domain.RelationKey]Property, pending map[domain.RelationKey]string, self domain.RelationKey) bool {
	for _, key := range referencedKeys(text) {
		if key == self {
			return true
		}
		if _, ok := pending[key]; ok {
			return true
		}
	}
	return false
}

func referencedKeys(text string) []domain.RelationKey {
	var keys []domain.RelationKey
	for _, m := range propRef.FindAllStringSubmatch(text, -1) {
		keys = append(keys, domain.RelationKey(m[1]))
	}
	for _, m := range relRef.FindAllStringSubmatch(text, -1) {
		keys = append(keys, domain.RelationKey(m[1]), domain.RelationKey(m[2]))
	}
	return keys
}

func inCycle(start domain.RelationKey, formulas map[domain.RelationKey]*Formula) bool {
	seen := map[domain.RelationKey]bool{}
	var walk func(k domain.RelationKey) bool
	walk = func(k domain.RelationKey) bool {
		f, ok := formulas[k]
		if !ok {
			return false
		}
		for _, d := range f.deps {
			if d.Key == start {
				return true
			}
			if seen[d.Key] {
				continue
			}
			seen[d.Key] = true
			if walk(d.Key) {
				return true
			}
		}
		return false
	}
	return walk(start)
}

func schemaOf(props map[domain.RelationKey]Property) *Schema {
	all := make([]Property, 0, len(props))
	for _, p := range props {
		all = append(all, p)
	}
	return NewSchema(all...)
}

// Result holds the formula values computed for one object.
type Result struct {
	// Keys are all formula properties of the object, with or without a value.
	Keys   []domain.RelationKey
	Values map[domain.RelationKey]domain.Value
	Errors map[domain.RelationKey]error
}

// Compute evaluates the formula properties that apply to an object: the ones
// its type recommends plus extraKeys (relations added to the object itself).
// details are the object's current details; related objects are read from store.
func Compute(store Store, details *domain.Details, extraKeys []domain.RelationKey) (*Result, error) {
	sf, err := loadSpace(store)
	if err != nil {
		return nil, err
	}
	if len(sf.formulas)+len(sf.errors) == 0 {
		return &Result{}, nil
	}
	keys, err := sf.objectFormulaKeys(store, details, extraKeys)
	if err != nil {
		return nil, err
	}
	res := &Result{
		Keys:   keys,
		Values: make(map[domain.RelationKey]domain.Value, len(keys)),
		Errors: map[domain.RelationKey]error{},
	}
	src := &detailsSource{store: store, schema: sf.schema, details: details, computed: res.Values}
	// formulas reading other formulas of the same object need them first
	for _, key := range orderByDeps(keys, sf.formulas) {
		if err, ok := sf.errors[key]; ok {
			res.Errors[key] = err
			continue
		}
		f := sf.formulas[key]
		v, err := f.Evaluate(src)
		if err != nil {
			res.Errors[key] = err
			continue
		}
		res.Values[key] = v
	}
	return res, nil
}

func (sf *spaceFormulas) objectFormulaKeys(store Store, details *domain.Details, extraKeys []domain.RelationKey) ([]domain.RelationKey, error) {
	seen := map[domain.RelationKey]bool{}
	var keys []domain.RelationKey
	add := func(key domain.RelationKey) {
		if seen[key] {
			return
		}
		if _, ok := sf.formulas[key]; !ok {
			if _, ok := sf.errors[key]; !ok {
				return
			}
		}
		seen[key] = true
		keys = append(keys, key)
	}

	if typeId := details.GetString(bundle.RelationKeyType); typeId != "" {
		typeDetails, err := store.GetDetails(typeId)
		if err != nil {
			return nil, fmt.Errorf("get type details: %w", err)
		}
		for _, listKey := range []domain.RelationKey{
			bundle.RelationKeyRecommendedRelations,
			bundle.RelationKeyRecommendedFeaturedRelations,
			bundle.RelationKeyRecommendedHiddenRelations,
		} {
			for _, id := range typeDetails.GetStringList(listKey) {
				if key, ok := sf.keyById[id]; ok {
					add(key)
				}
			}
		}
	}
	for _, key := range extraKeys {
		add(key)
	}
	return keys, nil
}

// orderByDeps puts formulas that read other formulas of the object after them.
func orderByDeps(keys []domain.RelationKey, formulas map[domain.RelationKey]*Formula) []domain.RelationKey {
	inSet := make(map[domain.RelationKey]bool, len(keys))
	for _, k := range keys {
		inSet[k] = true
	}
	done := map[domain.RelationKey]bool{}
	out := make([]domain.RelationKey, 0, len(keys))
	var visit func(k domain.RelationKey)
	visit = func(k domain.RelationKey) {
		if done[k] {
			return
		}
		done[k] = true
		if f, ok := formulas[k]; ok {
			for _, d := range f.deps {
				if d.Rel == "" && inSet[d.Key] {
					visit(d.Key)
				}
			}
		}
		out = append(out, k)
	}
	for _, k := range keys {
		visit(k)
	}
	return out
}

// detailsSource reads values for a formula from an object's details and, for
// related objects, from the store.
type detailsSource struct {
	store    Store
	schema   *Schema
	details  *domain.Details
	computed map[domain.RelationKey]domain.Value
}

func (s *detailsSource) Value(key domain.RelationKey) any {
	if v, ok := s.computed[key]; ok {
		return s.convert(key, v)
	}
	return s.convert(key, s.details.Get(key))
}

func (s *detailsSource) Related(rel, key domain.RelationKey) []any {
	ids := s.details.GetStringList(rel)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		d, err := s.store.GetDetails(id)
		if err != nil || d == nil || d.Len() == 0 {
			continue
		}
		out = append(out, s.convert(key, d.Get(key)))
	}
	return out
}

// convert turns a detail value into the Go value formulas see (see Source).
func (s *detailsSource) convert(key domain.RelationKey, v domain.Value) any {
	if !v.Ok() || v.IsNull() {
		return nil
	}
	p, ok := s.schema.ByKey(key)
	if !ok {
		return nil
	}
	switch p.valueFormat() {
	case model.RelationFormat_number:
		return v.Float64()
	case model.RelationFormat_checkbox:
		return v.Bool()
	case model.RelationFormat_date:
		if ts := v.Int64(); ts != 0 {
			return time.Unix(ts, 0).UTC()
		}
		return nil
	case model.RelationFormat_status:
		names := s.optionNames(v.WrapToStringList())
		if len(names) == 0 {
			return nil
		}
		return names[0]
	case model.RelationFormat_tag:
		return s.optionNames(v.WrapToStringList())
	case model.RelationFormat_object, model.RelationFormat_file:
		return v.WrapToStringList()
	default:
		return v.String()
	}
}

func (s *detailsSource) optionNames(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		d, err := s.store.GetDetails(id)
		if err != nil || d == nil {
			continue
		}
		if name := d.GetString(bundle.RelationKeyName); name != "" {
			names = append(names, name)
		}
	}
	return names
}
