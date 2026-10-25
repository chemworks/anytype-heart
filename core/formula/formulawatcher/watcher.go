// Package formulawatcher recomputes formula properties of objects when values
// they read through relations change in other objects.
package formulawatcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/anyproto/any-sync/app"
	"github.com/anyproto/any-sync/app/logger"
	"github.com/anyproto/any-sync/app/ocache"
	"go.uber.org/zap"

	"github.com/anyproto/anytype-heart/core/block/editor/smartblock"
	"github.com/anyproto/anytype-heart/core/block/editor/state"
	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/core/formula"
	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/database"
	"github.com/anyproto/anytype-heart/pkg/lib/localstore/objectstore"
	"github.com/anyproto/anytype-heart/pkg/lib/localstore/objectstore/spaceindex"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/anyproto/anytype-heart/space"
)

const (
	CName = "formula-watcher"

	defaultAggregationInterval = 200 * time.Millisecond
)

var log = logger.NewNamed(CName)

type jobKind int

const (
	// objectChanged: recompute objects that link to the object.
	objectChanged jobKind = iota
	// relationChanged: a formula property changed, recompute objects of the types that use it.
	relationChanged
	// typeChanged: a type changed its properties, recompute its objects.
	typeChanged
)

type job struct {
	kind    jobKind
	spaceId string
	id      string
}

type Watcher interface {
	app.ComponentRunnable
}

type watcher struct {
	store        objectstore.ObjectStore
	spaceService space.Service

	aggregationInterval time.Duration

	lock    sync.Mutex
	pending map[job]struct{}
	wakeCh  chan struct{}

	ctx       context.Context
	cancelCtx context.CancelFunc
	done      chan struct{}
}

func New() Watcher {
	return &watcher{}
}

func (w *watcher) Name() string {
	return CName
}

func (w *watcher) Init(a *app.App) error {
	w.store = app.MustComponent[objectstore.ObjectStore](a)
	w.spaceService = app.MustComponent[space.Service](a)
	w.aggregationInterval = defaultAggregationInterval
	w.pending = map[job]struct{}{}
	w.wakeCh = make(chan struct{}, 1)
	return nil
}

func (w *watcher) Run(context.Context) error {
	w.ctx, w.cancelCtx = context.WithCancel(context.Background())
	w.done = make(chan struct{})
	smartblock.SetFormulaIndexedHook(w.onIndexed)
	go w.loop()
	return nil
}

func (w *watcher) Close(context.Context) error {
	smartblock.SetFormulaIndexedHook(nil)
	if w.cancelCtx != nil {
		w.cancelCtx()
	}
	if w.done != nil {
		select {
		case <-w.done:
		case <-time.After(time.Second):
			log.Warn("formula watcher did not finish in time")
		}
	}
	return nil
}

// onIndexed runs inside the object's apply, so it only queues work.
func (w *watcher) onIndexed(spaceId, objectId string, details *domain.Details) {
	j := job{kind: objectChanged, spaceId: spaceId, id: objectId}
	switch model.ObjectTypeLayout(details.GetInt64(bundle.RelationKeyResolvedLayout)) {
	case model.ObjectType_relation:
		// name, format or formula may have changed: compiled formulas are stale
		formula.Invalidate(spaceId)
		if model.RelationFormat(details.GetInt64(bundle.RelationKeyRelationFormat)) != model.RelationFormat_formula {
			return
		}
		j.kind = relationChanged
	case model.ObjectType_objectType:
		j.kind = typeChanged
	}
	w.enqueue(j)
}

func (w *watcher) enqueue(j job) {
	w.lock.Lock()
	w.pending[j] = struct{}{}
	w.lock.Unlock()
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *watcher) loop() {
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wakeCh:
		}
		// let a burst of edits settle before recomputing
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(w.aggregationInterval):
		}
		w.processPending()
	}
}

func (w *watcher) processPending() {
	w.lock.Lock()
	jobs := w.pending
	w.pending = map[job]struct{}{}
	w.lock.Unlock()

	for j := range jobs {
		if err := w.process(j); err != nil {
			log.Error("recompute formulas", zap.String("objectId", j.id), zap.Error(err))
		}
	}
}

func (w *watcher) process(j job) error {
	idx := w.store.SpaceIndex(j.spaceId)
	has, err := formula.HasFormulas(idx)
	if err != nil {
		return fmt.Errorf("load formulas: %w", err)
	}
	if !has {
		return nil
	}

	var targets []string
	switch j.kind {
	case objectChanged:
		targets, err = idx.GetInboundLinksById(j.id)
		if err != nil {
			return fmt.Errorf("get inbound links: %w", err)
		}
	case relationChanged:
		targets, err = objectsOfTypesUsing(idx, j.id)
		if err != nil {
			return fmt.Errorf("list objects using relation: %w", err)
		}
	case typeChanged:
		targets, err = objectsOfType(idx, j.id)
		if err != nil {
			return fmt.Errorf("list objects of type: %w", err)
		}
	}

	for _, id := range targets {
		if id == j.id {
			continue
		}
		if err := w.recompute(j.spaceId, idx, id); err != nil {
			log.Warn("recompute formulas of object", zap.String("objectId", id), zap.Error(err))
		}
	}
	return nil
}

// recompute updates the formula values of one object. Objects that are not open
// are updated in the index directly; open ones re-run their own injection, so
// that their in-memory state stays the source of truth.
func (w *watcher) recompute(spaceId string, idx spaceindex.Store, id string) error {
	details, err := idx.GetDetails(id)
	if err != nil {
		return fmt.Errorf("get details: %w", err)
	}
	if details == nil || details.Len() == 0 {
		return nil
	}
	res, err := formula.Compute(idx, details, nil)
	if err != nil {
		return fmt.Errorf("compute: %w", err)
	}
	if len(res.Keys) == 0 || upToDate(details, res) {
		return nil
	}

	spc, err := w.spaceService.Get(w.ctx, spaceId)
	if err != nil {
		return fmt.Errorf("get space: %w", err)
	}
	modified := false
	err = spc.DoLockedIfNotExists(id, func() error {
		return idx.ModifyObjectDetails(id, func(current *domain.Details) (*domain.Details, bool, error) {
			if current == nil {
				return nil, false, nil
			}
			apply(current, res)
			modified = true
			return current, true, nil
		}, false)
	})
	if err == nil {
		if modified {
			// nothing indexed the object through a smartblock, so chain by hand
			w.enqueue(job{kind: objectChanged, spaceId: spaceId, id: id})
		}
		return nil
	}
	if !errors.Is(err, ocache.ErrExists) {
		return fmt.Errorf("update index: %w", err)
	}
	// the object is open: an empty state append re-runs injection and indexing
	if err = spc.Do(id, func(b smartblock.SmartBlock) error {
		return b.StateAppend(func(d state.Doc) (*state.State, []*pb.ChangeContent, error) {
			return d.NewState(), nil, nil
		})
	}); err != nil {
		return fmt.Errorf("refresh open object: %w", err)
	}
	return nil
}

func upToDate(details *domain.Details, res *formula.Result) bool {
	if !slices.Equal(details.GetStringList(bundle.RelationKeyFormulaErrors), res.ErrorList()) {
		return false
	}
	for _, key := range res.Keys {
		want, ok := res.Values[key]
		got := details.Get(key)
		if !ok {
			if got.Ok() {
				return false
			}
			continue
		}
		if !got.Ok() || !got.Equal(want) {
			return false
		}
	}
	return true
}

func apply(details *domain.Details, res *formula.Result) {
	for _, key := range res.Keys {
		if v, ok := res.Values[key]; ok {
			details.Set(key, v)
			continue
		}
		details.Delete(key)
	}
	if errs := res.ErrorList(); len(errs) > 0 {
		details.SetStringList(bundle.RelationKeyFormulaErrors, errs)
	} else {
		details.Delete(bundle.RelationKeyFormulaErrors)
	}
}

func objectsOfType(idx spaceindex.Store, typeId string) ([]string, error) {
	ids, _, err := idx.QueryObjectIds(database.Query{
		Filters: []database.FilterRequest{{
			RelationKey: bundle.RelationKeyType,
			Condition:   model.BlockContentDataviewFilter_Equal,
			Value:       domain.String(typeId),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("query objects: %w", err)
	}
	return ids, nil
}

func objectsOfTypesUsing(idx spaceindex.Store, relationId string) ([]string, error) {
	types, err := idx.Query(database.Query{
		Filters: []database.FilterRequest{{
			RelationKey: bundle.RelationKeyResolvedLayout,
			Condition:   model.BlockContentDataviewFilter_Equal,
			Value:       domain.Int64(model.ObjectType_objectType),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("query types: %w", err)
	}
	var ids []string
	for _, t := range types {
		if !recommends(t.Details, relationId) {
			continue
		}
		objs, err := objectsOfType(idx, t.Details.GetString(bundle.RelationKeyId))
		if err != nil {
			return nil, err
		}
		ids = append(ids, objs...)
	}
	return ids, nil
}

func recommends(typeDetails *domain.Details, relationId string) bool {
	for _, key := range []domain.RelationKey{
		bundle.RelationKeyRecommendedRelations,
		bundle.RelationKeyRecommendedFeaturedRelations,
		bundle.RelationKeyRecommendedHiddenRelations,
	} {
		for _, id := range typeDetails.GetStringList(key) {
			if id == relationId {
				return true
			}
		}
	}
	return false
}
