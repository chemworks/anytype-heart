package smartblock

import (
	"sync/atomic"

	"github.com/anyproto/anytype-heart/core/block/editor/state"
	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/core/formula"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/core/smartblock"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// FormulaIndexedHook is called after an object is indexed, so that objects whose
// formulas read it through relations can be recomputed. It must not block.
type FormulaIndexedHook func(spaceId, objectId string, details *domain.Details)

var formulaIndexedHook atomic.Pointer[FormulaIndexedHook]

// SetFormulaIndexedHook installs the hook; nil removes it.
func SetFormulaIndexedHook(hook FormulaIndexedHook) {
	if hook == nil {
		formulaIndexedHook.Store(nil)
		return
	}
	formulaIndexedHook.Store(&hook)
}

func notifyFormulaIndexed(spaceId, objectId string, details *domain.Details) {
	if hook := formulaIndexedHook.Load(); hook != nil {
		(*hook)(spaceId, objectId, details)
	}
}

// injectFormulaDetails computes the formula properties of the object and keeps
// them as local details: they are indexed and sent to clients, but never synced.
func (sb *smartBlock) injectFormulaDetails(s *state.State) {
	if sb.spaceIndex == nil {
		return
	}
	sb.injectFormulaValidation(s)
	var extraKeys []domain.RelationKey
	for _, link := range s.PickRelationLinks() {
		if link.Format == model.RelationFormat_formula {
			extraKeys = append(extraKeys, domain.RelationKey(link.Key))
		}
	}
	res, err := formula.Compute(sb.spaceIndex, s.CombinedDetails(), extraKeys)
	if err != nil {
		log.With("objectID", sb.Id()).Errorf("compute formulas: %v", err)
		return
	}
	for _, key := range res.Keys {
		if v, ok := res.Values[key]; ok {
			s.SetLocalDetail(key, v)
			continue
		}
		s.RemoveLocalDetail(key)
		if err := res.Errors[key]; err != nil {
			log.With("objectID", sb.Id(), "relationKey", key).Debugf("formula error: %v", err)
		}
	}
}

// injectFormulaValidation runs on formula property objects: it keeps the format
// of the formula's value and its compile error, so clients can show them.
func (sb *smartBlock) injectFormulaValidation(s *state.State) {
	if t := sb.Type(); t != smartblock.SmartBlockTypeRelation && t != smartblock.SmartBlockTypeBundledRelation {
		return
	}
	details := s.CombinedDetails()
	if model.RelationFormat(details.GetInt64(bundle.RelationKeyRelationFormat)) != model.RelationFormat_formula {
		return
	}
	key := domain.RelationKey(details.GetString(bundle.RelationKeyRelationKey))
	text := details.GetString(bundle.RelationKeyRelationFormula)
	format, err := formula.Validate(sb.spaceIndex, key, text)
	if err != nil {
		s.SetLocalDetail(bundle.RelationKeyRelationFormulaError, domain.String(err.Error()))
		s.RemoveLocalDetail(bundle.RelationKeyRelationFormulaResultFormat)
		return
	}
	s.SetLocalDetail(bundle.RelationKeyRelationFormulaError, domain.String(""))
	s.SetLocalDetail(bundle.RelationKeyRelationFormulaResultFormat, domain.Int64(int64(format)))
}
