package formula

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Dep is one value a formula reads: a property of the object itself (Rel is empty)
// or a property of the objects linked through the Rel relation.
type Dep struct {
	Rel domain.RelationKey
	Key domain.RelationKey
}

// Formula is a compiled, type-checked formula.
type Formula struct {
	program      *vm.Program
	deps         []Dep
	resultFormat model.RelationFormat
	env          map[string]any
}

func (f *Formula) Deps() []Dep {
	return f.deps
}

// ResultFormat is the relation format of the value the formula produces.
func (f *Formula) ResultFormat() model.RelationFormat {
	return f.resultFormat
}

// Error is a formula error with the position (1-based line and column) it refers to.
type Error struct {
	Line    int
	Column  int
	Message string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Message
	}
	return fmt.Sprintf("%s (line %d, column %d)", e.Message, e.Line, e.Column)
}

// Compile type-checks a formula in storage form (relation keys, see ToStorage).
func Compile(text string, schema *Schema) (*Formula, error) {
	if strings.TrimSpace(text) == "" {
		return nil, &Error{Message: "formula is empty"}
	}
	p := &refPatcher{text: text, schema: schema, env: map[string]any{}, seen: map[Dep]bool{}}

	// expr reads the env types when the Env option is built, so references are
	// collected on a first parse and patched again during compilation.
	tree, err := parser.Parse(text)
	if err != nil {
		return nil, toError(err)
	}
	ast.Walk(&tree.Node, p)
	if p.err != nil {
		return nil, p.err
	}

	opts := append([]expr.Option{
		expr.Env(p.env),
		expr.Patch(p),
	}, functions()...)

	program, err := expr.Compile(text, opts...)
	if p.err != nil {
		return nil, p.err
	}
	if err != nil {
		return nil, toError(err)
	}
	format, err := resultFormat(program.Node().Type())
	if err != nil {
		return nil, err
	}
	return &Formula{program: program, deps: p.deps, resultFormat: format, env: p.env}, nil
}

func toError(err error) error {
	var fileErr *file.Error
	if errors.As(err, &fileErr) {
		return &Error{Line: fileErr.Line, Column: fileErr.Column + 1, Message: fileErr.Message}
	}
	return &Error{Message: err.Error()}
}

var (
	timeType = reflect.TypeOf(time.Time{})
)

func resultFormat(t reflect.Type) (model.RelationFormat, error) {
	if t == nil {
		return 0, &Error{Message: "formula result has no type"}
	}
	if t == timeType {
		return model.RelationFormat_date, nil
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return model.RelationFormat_number, nil
	case reflect.String:
		return model.RelationFormat_shorttext, nil
	case reflect.Bool:
		return model.RelationFormat_checkbox, nil
	}
	return 0, &Error{Message: fmt.Sprintf("formula result must be a number, text, date or checkbox, got %s", typeName(t))}
}

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Interface {
		return "an unknown type"
	}
	if t.Kind() == reflect.Slice {
		return "a list"
	}
	return t.String()
}

// refPatcher replaces prop("key") and rel("relKey", "key") calls with typed
// identifiers, so that expr type-checks them, and records them as dependencies.
type refPatcher struct {
	text   string
	schema *Schema
	env    map[string]any
	deps   []Dep
	seen   map[Dep]bool
	err    error
}

func (p *refPatcher) Visit(node *ast.Node) {
	if p.err != nil {
		return
	}
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	callee, ok := call.Callee.(*ast.IdentifierNode)
	if !ok {
		return
	}
	switch callee.Value {
	case "prop":
		p.patchProp(node, call)
	case "rel":
		p.patchRel(node, call)
	}
}

func (p *refPatcher) patchProp(node *ast.Node, call *ast.CallNode) {
	args, ok := p.stringArgs(call, 1, `prop("Property")`)
	if !ok {
		return
	}
	prop, ok := p.schema.ByKey(domain.RelationKey(args[0]))
	if !ok {
		p.fail(call, fmt.Sprintf("property %q does not exist", args[0]))
		return
	}
	zero, ok := zeroValue(prop.valueFormat())
	if !ok {
		p.fail(call, fmt.Sprintf("property %q cannot be used in formulas", prop.Name))
		return
	}
	p.replace(node, call, Dep{Key: prop.Key}, zero)
}

func (p *refPatcher) patchRel(node *ast.Node, call *ast.CallNode) {
	args, ok := p.stringArgs(call, 2, `rel("Relation", "Property")`)
	if !ok {
		return
	}
	rel, ok := p.schema.ByKey(domain.RelationKey(args[0]))
	if !ok {
		p.fail(call, fmt.Sprintf("property %q does not exist", args[0]))
		return
	}
	if rel.Format != model.RelationFormat_object {
		p.fail(call, fmt.Sprintf("property %q is not a relation to other objects", rel.Name))
		return
	}
	prop, ok := p.schema.ByKey(domain.RelationKey(args[1]))
	if !ok {
		p.fail(call, fmt.Sprintf("property %q does not exist", args[1]))
		return
	}
	zero, ok := zeroList(prop.valueFormat())
	if !ok {
		p.fail(call, fmt.Sprintf("property %q cannot be used in formulas", prop.Name))
		return
	}
	p.replace(node, call, Dep{Rel: rel.Key, Key: prop.Key}, zero)
}

func (p *refPatcher) stringArgs(call *ast.CallNode, n int, usage string) ([]string, bool) {
	if len(call.Arguments) != n {
		p.fail(call, "expected "+usage)
		return nil, false
	}
	out := make([]string, 0, n)
	for _, arg := range call.Arguments {
		s, ok := arg.(*ast.StringNode)
		if !ok {
			p.fail(call, "expected "+usage+" with quoted names")
			return nil, false
		}
		out = append(out, s.Value)
	}
	return out, true
}

func (p *refPatcher) replace(node *ast.Node, call *ast.CallNode, dep Dep, zero any) {
	name := identifier(dep)
	p.env[name] = zero
	if !p.seen[dep] {
		p.seen[dep] = true
		p.deps = append(p.deps, dep)
	}
	ident := &ast.IdentifierNode{Value: name}
	ident.SetLocation(call.Location())
	ast.Patch(node, ident)
}

func (p *refPatcher) fail(call *ast.CallNode, msg string) {
	line, col := position(p.text, call.Location().From)
	p.err = &Error{Message: msg, Line: line, Column: col}
}

// position converts a rune offset into a 1-based line and column.
func position(text string, offset int) (line, col int) {
	line, col = 1, 1
	for i, r := range []rune(text) {
		if i == offset {
			break
		}
		if r == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}

func identifier(dep Dep) string {
	if dep.Rel == "" {
		return "p\x00" + string(dep.Key)
	}
	return "r\x00" + string(dep.Rel) + "\x00" + string(dep.Key)
}
