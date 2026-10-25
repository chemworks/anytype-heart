package formulawatcher

import (
	"context"
	"testing"
	"time"

	"github.com/anyproto/any-sync/app/ocache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/core/formula"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/localstore/objectstore"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/anyproto/anytype-heart/space/clientspace/mock_clientspace"
	"github.com/anyproto/anytype-heart/space/mock_space"
)

const spaceId = "spc1"

type fixture struct {
	*watcher
	store        *objectstore.StoreFixture
	spaceService *mock_space.MockService
	space        *mock_clientspace.MockSpace
}

func newFixture(t *testing.T) *fixture {
	formula.Invalidate(spaceId)
	t.Cleanup(func() { formula.Invalidate(spaceId) })

	store := objectstore.NewStoreFixture(t)
	spaceService := mock_space.NewMockService(t)
	spc := mock_clientspace.NewMockSpace(t)
	spaceService.EXPECT().Get(mock.Anything, spaceId).Return(spc, nil).Maybe()

	w := &watcher{
		store:               store,
		spaceService:        spaceService,
		aggregationInterval: time.Millisecond,
		pending:             map[job]struct{}{},
		parents:             map[job][]string{},
		wakeCh:              make(chan struct{}, 1),
	}
	w.ctx, w.cancelCtx = context.WithCancel(context.Background())
	t.Cleanup(w.cancelCtx)

	store.AddObjects(t, spaceId, []objectstore.TestObject{
		relation("hours", model.RelationFormat_number, ""),
		relation("tasks", model.RelationFormat_object, ""),
		relation("total", model.RelationFormat_formula, `sum(rel("tasks", "hours"))`),
		{
			bundle.RelationKeyId:                   domain.String("type-project"),
			bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
			bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-tasks", "rel-total"}),
		},
		{
			bundle.RelationKeyId:                   domain.String("type-task"),
			bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
			bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-hours"}),
		},
		{bundle.RelationKeyId: domain.String("task1"), bundle.RelationKeyType: domain.String("type-task"), "hours": domain.Float64(2)},
		{bundle.RelationKeyId: domain.String("task2"), bundle.RelationKeyType: domain.String("type-task"), "hours": domain.Float64(3)},
		{
			bundle.RelationKeyId:   domain.String("project1"),
			bundle.RelationKeyType: domain.String("type-project"),
			"tasks":                domain.StringList([]string{"task1", "task2"}),
			"total":                domain.Float64(5),
		},
	})
	idx := store.SpaceIndex(spaceId)
	require.NoError(t, idx.UpdateObjectLinks(context.Background(), "project1", []string{"task1", "task2"}))

	return &fixture{watcher: w, store: store, spaceService: spaceService, space: spc}
}

func relation(key string, format model.RelationFormat, text string) objectstore.TestObject {
	obj := objectstore.TestObject{
		bundle.RelationKeyId:             domain.String("rel-" + key),
		bundle.RelationKeyRelationKey:    domain.String(key),
		bundle.RelationKeyName:           domain.String(key),
		bundle.RelationKeyRelationFormat: domain.Int64(int64(format)),
		bundle.RelationKeyResolvedLayout: domain.Int64(int64(model.ObjectType_relation)),
	}
	if text != "" {
		obj[bundle.RelationKeyRelationFormula] = domain.String(text)
	}
	return obj
}

func (fx *fixture) setHours(t *testing.T, taskId string, hours float64) {
	t.Helper()
	err := fx.store.SpaceIndex(spaceId).ModifyObjectDetails(taskId, func(d *domain.Details) (*domain.Details, bool, error) {
		d.SetFloat64("hours", hours)
		return d, true, nil
	}, false)
	require.NoError(t, err)
}

func (fx *fixture) total(t *testing.T) float64 {
	t.Helper()
	d, err := fx.store.GetDetails(spaceId, "project1")
	require.NoError(t, err)
	return d.GetFloat64("total")
}

func TestWatcher_ObjectChanged(t *testing.T) {
	t.Run("closed project is updated in the index", func(t *testing.T) {
		// given
		fx := newFixture(t)
		fx.setHours(t, "task1", 10)
		fx.space.EXPECT().DoLockedIfNotExists("project1", mock.Anything).RunAndReturn(func(id string, f func() error) error {
			return f()
		})

		// when
		err := fx.process(job{kind: objectChanged, spaceId: spaceId, id: "task1"})

		// then
		require.NoError(t, err)
		assert.Equal(t, float64(13), fx.total(t))
		assert.Contains(t, fx.pending, job{kind: objectChanged, spaceId: spaceId, id: "project1"}, "chains to objects that read the project")
	})

	t.Run("open project is refreshed through its smartblock", func(t *testing.T) {
		// given
		fx := newFixture(t)
		fx.setHours(t, "task2", 1)
		fx.space.EXPECT().DoLockedIfNotExists("project1", mock.Anything).Return(ocache.ErrExists)
		fx.space.EXPECT().Do("project1", mock.Anything).Return(nil)

		// when
		err := fx.process(job{kind: objectChanged, spaceId: spaceId, id: "task2"})

		// then
		require.NoError(t, err)
		assert.Equal(t, float64(5), fx.total(t), "the smartblock, not the watcher, writes the open object")
	})

	t.Run("nothing to do when the value is already right", func(t *testing.T) {
		// given
		fx := newFixture(t)

		// when
		err := fx.process(job{kind: objectChanged, spaceId: spaceId, id: "task1"})

		// then
		require.NoError(t, err)
		assert.Empty(t, fx.pending)
	})
}

func TestWatcher_RelationChanged(t *testing.T) {
	// given
	fx := newFixture(t)
	fx.store.AddObjects(t, spaceId, []objectstore.TestObject{
		relation("total", model.RelationFormat_formula, `sum(rel("tasks", "hours")) * 2`),
	})
	formula.Invalidate(spaceId)
	fx.space.EXPECT().DoLockedIfNotExists("project1", mock.Anything).RunAndReturn(func(id string, f func() error) error {
		return f()
	})

	// when
	err := fx.process(job{kind: relationChanged, spaceId: spaceId, id: "rel-total"})

	// then
	require.NoError(t, err)
	assert.Equal(t, float64(10), fx.total(t))
}

func TestWatcher_OnIndexed(t *testing.T) {
	t.Run("relation change invalidates and queues its types", func(t *testing.T) {
		// given
		fx := newFixture(t)
		details := domain.NewDetailsFromMap(map[domain.RelationKey]domain.Value{
			bundle.RelationKeyResolvedLayout: domain.Int64(int64(model.ObjectType_relation)),
			bundle.RelationKeyRelationFormat: domain.Int64(int64(model.RelationFormat_formula)),
		})

		// when
		fx.onIndexed(spaceId, "rel-total", details)

		// then
		assert.Contains(t, fx.pending, job{kind: relationChanged, spaceId: spaceId, id: "rel-total"})
	})

	t.Run("plain object queues its dependents", func(t *testing.T) {
		// given
		fx := newFixture(t)

		// when
		fx.onIndexed(spaceId, "task1", domain.NewDetails())

		// then
		assert.Contains(t, fx.pending, job{kind: objectChanged, spaceId: spaceId, id: "task1"})
	})
}

func TestWatcher_Children(t *testing.T) {
	// given
	fx := newFixture(t)
	fx.store.AddObjects(t, spaceId, []objectstore.TestObject{
		relation("project", model.RelationFormat_object, ""),
		relation("pending", model.RelationFormat_formula, `sum(children("project", "hours"))`),
		{
			bundle.RelationKeyId:                   domain.String("type-project"),
			bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
			bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-pending"}),
		},
		{bundle.RelationKeyId: domain.String("projectA"), bundle.RelationKeyType: domain.String("type-project")},
		{bundle.RelationKeyId: domain.String("projectB"), bundle.RelationKeyType: domain.String("type-project")},
		{bundle.RelationKeyId: domain.String("t1"), "project": domain.StringList([]string{"projectA"}), "hours": domain.Float64(4)},
	})
	formula.Invalidate(spaceId)
	fx.space.EXPECT().DoLockedIfNotExists(mock.Anything, mock.Anything).RunAndReturn(func(id string, f func() error) error {
		return f()
	})
	pending := func(id string) float64 {
		d, err := fx.store.GetDetails(spaceId, id)
		require.NoError(t, err)
		return d.GetFloat64("pending")
	}
	moveTo := func(projectId string) {
		err := fx.store.SpaceIndex(spaceId).ModifyObjectDetails("t1", func(d *domain.Details) (*domain.Details, bool, error) {
			d.SetStringList("project", []string{projectId})
			return d, true, nil
		}, false)
		require.NoError(t, err)
	}

	t.Run("task change updates its project", func(t *testing.T) {
		// when
		err := fx.process(job{kind: objectChanged, spaceId: spaceId, id: "t1"})

		// then
		require.NoError(t, err)
		assert.Equal(t, float64(4), pending("projectA"))
	})

	t.Run("moved task updates the old and the new project", func(t *testing.T) {
		// given
		moveTo("projectB")

		// when
		err := fx.process(job{kind: objectChanged, spaceId: spaceId, id: "t1"})

		// then
		require.NoError(t, err)
		assert.Equal(t, float64(0), pending("projectA"))
		assert.Equal(t, float64(4), pending("projectB"))
	})

	t.Run("archived project keeps its last value", func(t *testing.T) {
		// given
		err := fx.store.SpaceIndex(spaceId).ModifyObjectDetails("projectB", func(d *domain.Details) (*domain.Details, bool, error) {
			d.SetBool(bundle.RelationKeyIsArchived, true)
			return d, true, nil
		}, false)
		require.NoError(t, err)
		moveTo("projectA")

		// when
		err = fx.process(job{kind: objectChanged, spaceId: spaceId, id: "t1"})

		// then
		require.NoError(t, err)
		assert.Equal(t, float64(4), pending("projectA"))
		assert.Equal(t, float64(4), pending("projectB"))
	})
}
