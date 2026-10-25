package formula

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

const (
	keyPrice    = domain.RelationKey("price")
	keyQty      = domain.RelationKey("qty")
	keyStatus   = domain.RelationKey("status")
	keyDue      = domain.RelationKey("due")
	keyDone     = domain.RelationKey("done")
	keyTasks    = domain.RelationKey("tasks")
	keyHours    = domain.RelationKey("hours")
	keyTotal    = domain.RelationKey("total")
	keyCost     = domain.RelationKey("cost")
	keyTaskName = domain.RelationKey("name")
)

func testSchema() *Schema {
	return NewSchema(
		Property{Key: keyPrice, Name: "Precio", Format: model.RelationFormat_number},
		Property{Key: keyQty, Name: "Cantidad", Format: model.RelationFormat_number},
		Property{Key: keyStatus, Name: "Estado", Format: model.RelationFormat_shorttext},
		Property{Key: keyDue, Name: "Vence", Format: model.RelationFormat_date},
		Property{Key: keyDone, Name: "Hecho", Format: model.RelationFormat_checkbox},
		Property{Key: keyTasks, Name: "Tareas", Format: model.RelationFormat_object},
		Property{Key: keyHours, Name: "Horas", Format: model.RelationFormat_number},
		Property{Key: keyTaskName, Name: "Nombre", Format: model.RelationFormat_shorttext},
		Property{Key: keyTotal, Name: "Total", Format: FormatFormula, ResultFormat: model.RelationFormat_number},
		Property{Key: keyCost, Name: "Costo", Format: FormatFormula, ResultFormat: model.RelationFormat_number},
	)
}

type fakeSource struct {
	values  map[domain.RelationKey]any
	related map[domain.RelationKey][]map[domain.RelationKey]any
}

func (s fakeSource) Value(key domain.RelationKey) any {
	return s.values[key]
}

func (s fakeSource) Related(rel, key domain.RelationKey) []any {
	var out []any
	for _, obj := range s.related[rel] {
		out = append(out, obj[key])
	}
	return out
}

func project(hours ...float64) fakeSource {
	src := fakeSource{
		values:  map[domain.RelationKey]any{keyPrice: 12.5, keyQty: 4.0, keyStatus: "Hecho"},
		related: map[domain.RelationKey][]map[domain.RelationKey]any{},
	}
	for i, h := range hours {
		src.related[keyTasks] = append(src.related[keyTasks], map[domain.RelationKey]any{
			keyHours:    h,
			keyTaskName: string(rune('A' + i)),
			keyDone:     h > 2,
		})
	}
	return src
}

func compileDisplay(t *testing.T, text string) *Formula {
	t.Helper()
	schema := testSchema()
	stored, err := ToStorage(text, schema)
	require.NoError(t, err)
	f, err := Compile(stored, schema)
	require.NoError(t, err)
	return f
}

func TestEvaluate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		formula    string
		src        fakeSource
		want       domain.Value
		wantFormat model.RelationFormat
	}{
		{
			name:       "arithmetic on own properties",
			formula:    `prop("Precio") * prop("Cantidad")`,
			src:        project(),
			want:       domain.Float64(50),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "ternary on text",
			formula:    `prop("Estado") == "Hecho" ? "OK" : "Pendiente"`,
			src:        project(),
			want:       domain.String("OK"),
			wantFormat: model.RelationFormat_shorttext,
		},
		{
			name:       "sum of a property of related objects",
			formula:    `sum(rel("Tareas", "Horas"))`,
			src:        project(2, 3, 5),
			want:       domain.Float64(10),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "count of related objects",
			formula:    `len(prop("Tareas"))`,
			src:        fakeSource{values: map[domain.RelationKey]any{keyTasks: []string{"a", "b"}}},
			want:       domain.Float64(2),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "average with rounding",
			formula:    `round(avg(rel("Tareas", "Horas")), 2)`,
			src:        project(1, 1, 2),
			want:       domain.Float64(1.33),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "filtered count over related checkboxes",
			formula:    `count(rel("Tareas", "Hecho"), # == true)`,
			src:        project(1, 3, 5),
			want:       domain.Float64(2),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "join related names",
			formula:    `join(rel("Tareas", "Nombre"), ", ")`,
			src:        project(1, 2),
			want:       domain.String("A, B"),
			wantFormat: model.RelationFormat_shorttext,
		},
		{
			name:       "max of related numbers",
			formula:    `max(rel("Tareas", "Horas"))`,
			src:        project(4, 9, 1),
			want:       domain.Float64(9),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "max of two numbers",
			formula:    `max(prop("Precio"), 20)`,
			src:        project(),
			want:       domain.Float64(20),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:    "reads another formula through a relation",
			formula: `sum(rel("Tareas", "Costo"))`,
			src: fakeSource{related: map[domain.RelationKey][]map[domain.RelationKey]any{
				keyTasks: {{keyCost: 10.0}, {keyCost: 2.5}},
			}},
			want:       domain.Float64(12.5),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "missing number counts as zero",
			formula:    `prop("Precio") + 1`,
			src:        fakeSource{},
			want:       domain.Float64(1),
			wantFormat: model.RelationFormat_number,
		},
		{
			name:       "empty relation sums to zero",
			formula:    `sum(rel("Tareas", "Horas"))`,
			src:        project(),
			want:       domain.Float64(0),
			wantFormat: model.RelationFormat_number,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			f := compileDisplay(t, tc.formula)

			// when
			got, err := f.Evaluate(tc.src)

			// then
			require.NoError(t, err)
			assert.Equal(t, tc.wantFormat, f.ResultFormat())
			assert.True(t, tc.want.Equal(got), "want %v, got %v", tc.want, got)
		})
	}
}

func TestEvaluateDates(t *testing.T) {
	due := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	src := fakeSource{values: map[domain.RelationKey]any{keyDue: due}}

	t.Run("dateAdd returns a date", func(t *testing.T) {
		// given
		f := compileDisplay(t, `dateAdd(prop("Vence"), 1, "months")`)

		// when
		got, err := f.Evaluate(src)

		// then
		require.NoError(t, err)
		assert.Equal(t, model.RelationFormat_date, f.ResultFormat())
		assert.Equal(t, time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC).Unix(), got.Int64())
	})

	t.Run("formatDate", func(t *testing.T) {
		// given
		f := compileDisplay(t, `formatDate(prop("Vence"), "DD/MM/YYYY")`)

		// when
		got, err := f.Evaluate(src)

		// then
		require.NoError(t, err)
		assert.Equal(t, "10/11/2026", got.String())
	})

	t.Run("dateBetween in days", func(t *testing.T) {
		// given
		f := compileDisplay(t, `dateBetween(prop("Vence"), date("2026-10-25"), "days")`)

		// when
		got, err := f.Evaluate(src)

		// then
		require.NoError(t, err)
		assert.Equal(t, due.Sub(start).Hours()/24, got.Float64())
	})
}

func TestCompileErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		formula string
		wantMsg string
	}{
		{
			name:    "empty formula",
			formula: `  `,
			wantMsg: "formula is empty",
		},
		{
			name:    "syntax error",
			formula: `prop("price") *`,
			wantMsg: "unexpected token",
		},
		{
			name:    "unknown property",
			formula: `prop("nope")`,
			wantMsg: `property "nope" does not exist`,
		},
		{
			name:    "rel over a non-object property",
			formula: `sum(rel("price", "hours"))`,
			wantMsg: `property "Precio" is not a relation to other objects`,
		},
		{
			name:    "type mismatch",
			formula: `prop("status") * 2`,
			wantMsg: "invalid operation",
		},
		{
			name:    "list result is not allowed",
			formula: `rel("tasks", "hours")`,
			wantMsg: "formula result must be a number, text, date or checkbox, got a list",
		},
		{
			name:    "non-literal property name",
			formula: `prop("pri" + "ce")`,
			wantMsg: "with quoted names",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// when
			_, err := Compile(tc.formula, testSchema())

			// then
			require.Error(t, err)
			var fErr *Error
			require.ErrorAs(t, err, &fErr)
			assert.Contains(t, fErr.Message, tc.wantMsg)
		})
	}

	t.Run("error position points at the reference", func(t *testing.T) {
		// when
		_, err := Compile("1 +\n  prop(\"nope\")", testSchema())

		// then
		var fErr *Error
		require.ErrorAs(t, err, &fErr)
		assert.Equal(t, 2, fErr.Line)
		assert.Equal(t, 3, fErr.Column)
	})
}

func TestEvaluateErrors(t *testing.T) {
	t.Run("division by zero", func(t *testing.T) {
		// given
		f := compileDisplay(t, `prop("Precio") / prop("Cantidad")`)

		// when
		_, err := f.Evaluate(fakeSource{values: map[domain.RelationKey]any{keyPrice: 1.0}})

		// then
		require.EqualError(t, err, "division by zero")
	})

	t.Run("avg of no related objects", func(t *testing.T) {
		// given
		f := compileDisplay(t, `avg(rel("Tareas", "Horas"))`)

		// when
		_, err := f.Evaluate(project())

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "avg of an empty list")
	})
}

func TestNames(t *testing.T) {
	schema := testSchema()

	t.Run("round trip between names and keys", func(t *testing.T) {
		// given
		display := `sum(rel("Tareas", "Horas")) * prop("Precio")`

		// when
		stored, err := ToStorage(display, schema)

		// then
		require.NoError(t, err)
		assert.Equal(t, `sum(rel("tasks", "hours")) * prop("price")`, stored)
		assert.Equal(t, display, ToDisplay(stored, schema))
	})

	t.Run("renamed property keeps working", func(t *testing.T) {
		// given
		stored, err := ToStorage(`prop("Horas") * 2`, schema)
		require.NoError(t, err)
		renamed := NewSchema(Property{Key: keyHours, Name: "Hs", Format: model.RelationFormat_number})

		// when
		f, err := Compile(stored, renamed)

		// then
		require.NoError(t, err)
		assert.Equal(t, `prop("Hs") * 2`, ToDisplay(stored, renamed))
		got, err := f.Evaluate(fakeSource{values: map[domain.RelationKey]any{keyHours: 3.0}})
		require.NoError(t, err)
		assert.Equal(t, float64(6), got.Float64())
	})

	t.Run("unknown name", func(t *testing.T) {
		// when
		_, err := ToStorage(`prop("Nada")`, schema)

		// then
		require.EqualError(t, err, `property "Nada" does not exist`)
	})
}

func TestCheckCycles(t *testing.T) {
	schema := testSchema()
	compile := func(text string) *Formula {
		f, err := Compile(text, schema)
		require.NoError(t, err)
		return f
	}

	t.Run("no cycle", func(t *testing.T) {
		// given
		formulas := map[domain.RelationKey]*Formula{
			keyCost:  compile(`prop("hours") * 10`),
			keyTotal: compile(`sum(rel("tasks", "cost"))`),
		}

		// when
		err := CheckCycles(formulas, schema)

		// then
		require.NoError(t, err)
	})

	t.Run("cycle through a relation", func(t *testing.T) {
		// given
		formulas := map[domain.RelationKey]*Formula{
			keyCost:  compile(`prop("total") + 1`),
			keyTotal: compile(`sum(rel("tasks", "cost"))`),
		}

		// when
		err := CheckCycles(formulas, schema)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "formulas depend on each other")
	})

	t.Run("formula that reads itself", func(t *testing.T) {
		// given
		formulas := map[domain.RelationKey]*Formula{
			keyTotal: compile(`prop("total") + 1`),
		}

		// when
		err := CheckCycles(formulas, schema)

		// then
		require.EqualError(t, err, "formulas depend on each other: Total -> Total")
	})
}

func TestDeps(t *testing.T) {
	// given
	f := compileDisplay(t, `sum(rel("Tareas", "Horas")) + prop("Precio") * prop("Precio")`)

	// when
	got := f.Deps()

	// then
	want := []Dep{{Rel: keyTasks, Key: keyHours}, {Key: keyPrice}}
	assert.ElementsMatch(t, want, got)
}
