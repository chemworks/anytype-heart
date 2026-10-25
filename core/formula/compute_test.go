package formula

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/localstore/objectstore"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

const testSpaceId = "space1"

func relationObject(key, name string, format model.RelationFormat, formula string) objectstore.TestObject {
	obj := objectstore.TestObject{
		bundle.RelationKeyId:             domain.String("rel-" + key),
		bundle.RelationKeyRelationKey:    domain.String(key),
		bundle.RelationKeyName:           domain.String(name),
		bundle.RelationKeyRelationFormat: domain.Int64(int64(format)),
		bundle.RelationKeyResolvedLayout: domain.Int64(int64(model.ObjectType_relation)),
		bundle.RelationKeyLayout:         domain.Int64(int64(model.ObjectType_relation)),
	}
	if formula != "" {
		obj[bundle.RelationKeyRelationFormula] = domain.String(formula)
	}
	return obj
}

func typeObject(id string, relationKeys ...string) objectstore.TestObject {
	ids := make([]string, 0, len(relationKeys))
	for _, k := range relationKeys {
		ids = append(ids, "rel-"+k)
	}
	return objectstore.TestObject{
		bundle.RelationKeyId:                   domain.String(id),
		bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
		bundle.RelationKeyRecommendedRelations: domain.StringList(ids),
	}
}

type computeFixture struct {
	store *objectstore.StoreFixture
}

func newComputeFixture(t *testing.T) *computeFixture {
	Invalidate(testSpaceId)
	t.Cleanup(func() { Invalidate(testSpaceId) })
	store := objectstore.NewStoreFixture(t)
	store.AddObjects(t, testSpaceId, []objectstore.TestObject{
		relationObject("hours", "Horas", model.RelationFormat_number, ""),
		relationObject("tasks", "Tareas", model.RelationFormat_object, ""),
		relationObject("state", "Estado", model.RelationFormat_status, ""),
		relationObject("cost", "Costo", model.RelationFormat_formula, `prop("hours") * 10`),
		relationObject("total", "Total", model.RelationFormat_formula, `sum(rel("tasks", "hours"))`),
		relationObject("totalCost", "Costo total", model.RelationFormat_formula, `sum(rel("tasks", "cost"))`),
		relationObject("label", "Etiqueta", model.RelationFormat_formula, `prop("total") > 4 ? "grande" : "chico"`),
		typeObject("type-task", "hours", "cost", "state"),
		typeObject("type-project", "tasks", "total", "totalCost", "label"),
		{
			bundle.RelationKeyId:   domain.String("opt-done"),
			bundle.RelationKeyName: domain.String("Hecho"),
		},
		{
			bundle.RelationKeyId:   domain.String("task1"),
			bundle.RelationKeyType: domain.String("type-task"),
			"hours":                domain.Float64(2),
			"cost":                 domain.Float64(20),
			"state":                domain.String("opt-done"),
		},
		{
			bundle.RelationKeyId:   domain.String("task2"),
			bundle.RelationKeyType: domain.String("type-task"),
			"hours":                domain.Float64(3),
			"cost":                 domain.Float64(30),
		},
	})
	return &computeFixture{store: store}
}

func (fx *computeFixture) compute(t *testing.T, details *domain.Details) *Result {
	t.Helper()
	res, err := Compute(fx.store.SpaceIndex(testSpaceId), details, nil)
	require.NoError(t, err)
	return res
}

func TestCompute(t *testing.T) {
	t.Run("project sums related tasks and chains formulas", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		project := domain.NewDetailsFromMap(map[domain.RelationKey]domain.Value{
			bundle.RelationKeyId:   domain.String("project1"),
			bundle.RelationKeyType: domain.String("type-project"),
			"tasks":                domain.StringList([]string{"task1", "task2"}),
		})

		// when
		res := fx.compute(t, project)

		// then
		require.Empty(t, res.Errors)
		assert.ElementsMatch(t, []domain.RelationKey{"total", "totalCost", "label"}, res.Keys)
		assert.Equal(t, float64(5), res.Values["total"].Float64())
		assert.Equal(t, float64(50), res.Values["totalCost"].Float64())
		assert.Equal(t, "grande", res.Values["label"].String())
	})

	t.Run("task computes its own formula and reads status names", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		task, err := fx.store.GetDetails(testSpaceId, "task1")
		require.NoError(t, err)
		task.SetFloat64("hours", 7)

		// when
		res := fx.compute(t, task)

		// then
		require.Empty(t, res.Errors)
		assert.Equal(t, float64(70), res.Values["cost"].Float64())
	})

	t.Run("object of a type without formulas", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		other := domain.NewDetailsFromMap(map[domain.RelationKey]domain.Value{
			bundle.RelationKeyId:   domain.String("note"),
			bundle.RelationKeyType: domain.String("type-task-missing"),
		})

		// when
		res := fx.compute(t, other)

		// then
		assert.Empty(t, res.Keys)
		assert.Empty(t, res.Values)
	})

	t.Run("status option names are visible to formulas", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		fx.store.AddObjects(t, testSpaceId, []objectstore.TestObject{
			relationObject("isDone", "Terminada", model.RelationFormat_formula, `prop("state") == "Hecho"`),
			typeObject("type-task", "hours", "cost", "state", "isDone"),
		})
		Invalidate(testSpaceId)
		task, err := fx.store.GetDetails(testSpaceId, "task1")
		require.NoError(t, err)

		// when
		res := fx.compute(t, task)

		// then
		require.Empty(t, res.Errors)
		assert.True(t, res.Values["isDone"].Bool())
	})

	t.Run("broken formula is reported, others still compute", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		fx.store.AddObjects(t, testSpaceId, []objectstore.TestObject{
			relationObject("broken", "Rota", model.RelationFormat_formula, `prop("nope") + 1`),
			typeObject("type-task", "hours", "cost", "broken"),
		})
		Invalidate(testSpaceId)
		task, err := fx.store.GetDetails(testSpaceId, "task1")
		require.NoError(t, err)

		// when
		res := fx.compute(t, task)

		// then
		assert.Equal(t, float64(20), res.Values["cost"].Float64())
		require.Contains(t, res.Errors, domain.RelationKey("broken"))
		assert.Contains(t, res.Errors["broken"].Error(), `property "nope" does not exist`)
	})

	t.Run("formulas in a loop are reported", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)
		fx.store.AddObjects(t, testSpaceId, []objectstore.TestObject{
			relationObject("a", "A", model.RelationFormat_formula, `prop("b") + 1`),
			relationObject("b", "B", model.RelationFormat_formula, `prop("a") + 1`),
			typeObject("type-task", "a", "b"),
		})
		Invalidate(testSpaceId)
		task, err := fx.store.GetDetails(testSpaceId, "task1")
		require.NoError(t, err)

		// when
		res := fx.compute(t, task)

		// then
		assert.Empty(t, res.Values)
		assert.Contains(t, res.Errors, domain.RelationKey("a"))
		assert.Contains(t, res.Errors, domain.RelationKey("b"))
	})
}

func TestValidate(t *testing.T) {
	t.Run("valid formula returns its result format", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)

		// when
		format, err := Validate(fx.store.SpaceIndex(testSpaceId), "newOne", `prop("total") > 1`)

		// then
		require.NoError(t, err)
		assert.Equal(t, model.RelationFormat_checkbox, format)
	})

	t.Run("formula that reads itself", func(t *testing.T) {
		// given
		fx := newComputeFixture(t)

		// when
		_, err := Validate(fx.store.SpaceIndex(testSpaceId), "total", `prop("total") + 1`)

		// then
		require.EqualError(t, err, "a formula cannot read itself")
	})
}
