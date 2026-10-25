package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anyproto/anytype-heart/core/block/simple"
	"github.com/anyproto/anytype-heart/core/block/simple/base"
	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Spike for formula properties: a computed value for a user-created relation key
// must be visible in the combined details (what gets indexed and sent to clients)
// but must never be written as a change, so it does not sync or enter history.
func TestFormulaSpike_LocalDetailForUserRelation(t *testing.T) {
	const formulaKey = domain.RelationKey("bafyreiformulatotalhours")

	t.Run("local detail is not in bundled local keys", func(t *testing.T) {
		assert.NotContains(t, bundle.LocalAndDerivedRelationKeys, formulaKey)
	})

	t.Run("computed value is combined but produces no change", func(t *testing.T) {
		// given
		d := NewDoc("root", map[string]simple.Block{
			"root": base.NewBase(&model.Block{Id: "root"}),
		})
		s := d.NewState()
		s.SetDetail(bundle.RelationKeyName, domain.String("Proyecto A"))
		_, _, err := ApplyState("", s, true)
		require.NoError(t, err)

		// when
		s = d.NewState()
		s.SetLocalDetail(formulaKey, domain.Float64(10))
		_, _, err = ApplyState("", s, true)
		require.NoError(t, err)

		// then
		assert.Equal(t, float64(10), d.(*State).CombinedDetails().GetFloat64(formulaKey))
		assert.False(t, d.(*State).Details().Has(formulaKey), "must not be a synced detail")
		for _, ch := range s.GetChanges() {
			if set := ch.GetDetailsSet(); set != nil {
				assert.NotEqual(t, string(formulaKey), set.Key, "must not produce a DetailsSet change")
			}
		}
	})

	t.Run("recomputed value replaces the previous one", func(t *testing.T) {
		// given
		d := NewDoc("root", map[string]simple.Block{
			"root": base.NewBase(&model.Block{Id: "root"}),
		})
		s := d.NewState()
		s.SetLocalDetail(formulaKey, domain.Float64(10))
		_, _, err := ApplyState("", s, true)
		require.NoError(t, err)

		// when
		s = d.NewState()
		s.SetLocalDetail(formulaKey, domain.Float64(6))
		_, _, err = ApplyState("", s, true)
		require.NoError(t, err)

		// then
		assert.Equal(t, float64(6), d.(*State).CombinedDetails().GetFloat64(formulaKey))
	})
}
