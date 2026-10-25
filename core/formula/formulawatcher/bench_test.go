package formulawatcher

import (
	"context"
	"fmt"
	"testing"
	"time"

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

// benchFixture is the acceptance case of the formula spec: projects whose
// formulas read their tasks, every object closed (updated in the index).
type benchFixture struct {
	*watcher
	store    *objectstore.StoreFixture
	projects []string
	tasks    [][]string
}

func newBenchFixture(tb testing.TB, projects, tasksPerProject int) *benchFixture {
	formula.Invalidate(spaceId)
	tb.Cleanup(func() { formula.Invalidate(spaceId) })

	store := objectstore.NewStoreFixture(tb)
	spaceService := mock_space.NewMockService(tb)
	spc := mock_clientspace.NewMockSpace(tb)
	spaceService.EXPECT().Get(mock.Anything, spaceId).Return(spc, nil).Maybe()
	spc.EXPECT().DoLockedIfNotExists(mock.Anything, mock.Anything).RunAndReturn(func(id string, f func() error) error {
		return f()
	}).Maybe()

	w := &watcher{
		store:               store,
		spaceService:        spaceService,
		aggregationInterval: time.Millisecond,
		pending:             map[job]struct{}{},
		parents:             map[job][]string{},
		wakeCh:              make(chan struct{}, 1),
	}
	w.ctx, w.cancelCtx = context.WithCancel(context.Background())
	tb.Cleanup(w.cancelCtx)

	objects := []objectstore.TestObject{
		relation("hours", model.RelationFormat_number, ""),
		relation("tasks", model.RelationFormat_object, ""),
		relation("cost", model.RelationFormat_formula, `prop("hours") * 10`),
		relation("total", model.RelationFormat_formula, `sum(rel("tasks", "hours"))`),
		relation("totalCost", model.RelationFormat_formula, `sum(rel("tasks", "cost"))`),
		{
			bundle.RelationKeyId:                   domain.String("type-project"),
			bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
			bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-tasks", "rel-total", "rel-totalCost"}),
		},
		{
			bundle.RelationKeyId:                   domain.String("type-task"),
			bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
			bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-hours", "rel-cost"}),
		},
	}
	fx := &benchFixture{watcher: w, store: store}
	for p := 0; p < projects; p++ {
		projectId := fmt.Sprintf("project%d", p)
		var taskIds []string
		for i := 0; i < tasksPerProject; i++ {
			taskId := fmt.Sprintf("task%d-%d", p, i)
			taskIds = append(taskIds, taskId)
			objects = append(objects, objectstore.TestObject{
				bundle.RelationKeyId:   domain.String(taskId),
				bundle.RelationKeyType: domain.String("type-task"),
				"hours":                domain.Float64(1),
				"cost":                 domain.Float64(10),
			})
		}
		objects = append(objects, objectstore.TestObject{
			bundle.RelationKeyId:   domain.String(projectId),
			bundle.RelationKeyType: domain.String("type-project"),
			"tasks":                domain.StringList(taskIds),
		})
		fx.projects = append(fx.projects, projectId)
		fx.tasks = append(fx.tasks, taskIds)
	}
	store.AddObjects(tb, spaceId, objects)
	idx := store.SpaceIndex(spaceId)
	for p, projectId := range fx.projects {
		require.NoError(tb, idx.UpdateObjectLinks(context.Background(), projectId, fx.tasks[p]))
	}
	return fx
}

func (fx *benchFixture) setHours(tb testing.TB, taskId string, hours float64) {
	err := fx.store.SpaceIndex(spaceId).ModifyObjectDetails(taskId, func(d *domain.Details) (*domain.Details, bool, error) {
		d.SetFloat64("hours", hours)
		return d, true, nil
	}, false)
	require.NoError(tb, err)
}

// drain processes queued jobs until none are left, like the loop does.
func (fx *benchFixture) drain(tb testing.TB) {
	for len(fx.pending) > 0 {
		fx.processPending()
	}
}

// BenchmarkTaskChanged: one task changes, its project is recomputed, and the
// chain it starts is followed to the end.
func BenchmarkTaskChanged(b *testing.B) {
	for _, size := range []struct{ projects, tasks int }{{50, 20}, {1, 1000}} {
		b.Run(fmt.Sprintf("%dx%d", size.projects, size.tasks), func(b *testing.B) {
			fx := newBenchFixture(b, size.projects, size.tasks)
			taskId := fx.tasks[0][0]
			i := 0
			for b.Loop() {
				i++
				fx.setHours(b, taskId, float64(i))
				require.NoError(b, fx.process(job{kind: objectChanged, spaceId: spaceId, id: taskId}))
				fx.drain(b)
			}
		})
	}
}

// BenchmarkFormulaEdited: the text of a formula changes, every object of the
// types that use it is recomputed.
func BenchmarkFormulaEdited(b *testing.B) {
	for _, rel := range []string{"total", "cost"} {
		b.Run(rel, func(b *testing.B) {
			fx := newBenchFixture(b, 50, 20)
			i := 0
			for b.Loop() {
				i++
				// change a value so that every recompute really writes
				for _, taskIds := range fx.tasks {
					fx.setHours(b, taskIds[0], float64(i))
				}
				formula.Invalidate(spaceId)
				require.NoError(b, fx.process(job{kind: relationChanged, spaceId: spaceId, id: "rel-" + rel}))
			}
		})
	}
}

// BenchmarkCompute: computing the formulas of one project, without writing.
func BenchmarkCompute(b *testing.B) {
	for _, tasks := range []int{20, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", tasks), func(b *testing.B) {
			fx := newBenchFixture(b, 1, tasks)
			idx := fx.store.SpaceIndex(spaceId)
			details, err := idx.GetDetails(fx.projects[0])
			require.NoError(b, err)
			for b.Loop() {
				_, err := formula.Compute(idx, details, nil)
				require.NoError(b, err)
			}
		})
	}
}

// BenchmarkCompileSpace: rebuilding the compiled formulas after any relation changes.
func BenchmarkCompileSpace(b *testing.B) {
	fx := newBenchFixture(b, 1, 1)
	idx := fx.store.SpaceIndex(spaceId)
	for b.Loop() {
		formula.Invalidate(spaceId)
		_, err := formula.HasFormulas(idx)
		require.NoError(b, err)
	}
}

// BenchmarkComputeChildren: one project summing the tasks that point to it,
// read through children() instead of a list on the project.
func BenchmarkComputeChildren(b *testing.B) {
	for _, tasks := range []int{20, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", tasks), func(b *testing.B) {
			fx := newBenchFixture(b, 1, tasks)
			projectId := fx.projects[0]
			objects := []objectstore.TestObject{
				relation("project", model.RelationFormat_object, ""),
				relation("pending", model.RelationFormat_formula, `sum(children("project", "hours"))`),
				{
					bundle.RelationKeyId:                   domain.String("type-project"),
					bundle.RelationKeyResolvedLayout:       domain.Int64(int64(model.ObjectType_objectType)),
					bundle.RelationKeyRecommendedRelations: domain.StringList([]string{"rel-pending"}),
				},
			}
			for _, taskId := range fx.tasks[0] {
				objects = append(objects, objectstore.TestObject{
					bundle.RelationKeyId:   domain.String(taskId),
					bundle.RelationKeyType: domain.String("type-task"),
					"hours":                domain.Float64(1),
					"project":              domain.StringList([]string{projectId}),
				})
			}
			fx.store.AddObjects(b, spaceId, objects)
			formula.Invalidate(spaceId)
			idx := fx.store.SpaceIndex(spaceId)
			details, err := idx.GetDetails(projectId)
			require.NoError(b, err)
			res, err := formula.Compute(idx, details, nil)
			require.NoError(b, err)
			require.Equal(b, float64(tasks), res.Values["pending"].Float64())
			for b.Loop() {
				_, err := formula.Compute(idx, details, nil)
				require.NoError(b, err)
			}
		})
	}
}
