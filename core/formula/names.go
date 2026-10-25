package formula

import (
	"fmt"
	"regexp"

	"github.com/anyproto/anytype-heart/core/domain"
)

// References are rewritten textually. A string literal that itself contains
// prop("...") would be rewritten too; that is accepted for now.
var (
	propRef = regexp.MustCompile(`prop\(\s*"([^"]*)"\s*\)`)
	relRef  = regexp.MustCompile(`rel\(\s*"([^"]*)"\s*,\s*"([^"]*)"\s*\)`)
)

// ToStorage rewrites property names to relation keys, which is the form formulas
// are stored and compiled in.
func ToStorage(text string, schema *Schema) (string, error) {
	return rewrite(text, func(name string) (string, error) {
		p, ok := schema.ByName(name)
		if !ok {
			return "", &Error{Message: fmt.Sprintf("property %q does not exist", name)}
		}
		return string(p.Key), nil
	})
}

// ToDisplay rewrites relation keys to the current property names. Keys that no
// longer exist are left as they are, so the user sees what is broken.
func ToDisplay(text string, schema *Schema) string {
	out, _ := rewrite(text, func(key string) (string, error) {
		if p, ok := schema.ByKey(domain.RelationKey(key)); ok {
			return p.Name, nil
		}
		return key, nil
	})
	return out
}

func rewrite(text string, mapName func(string) (string, error)) (string, error) {
	var firstErr error
	mapped := func(name string) string {
		out, err := mapName(name)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err != nil {
			return name
		}
		return out
	}
	text = relRef.ReplaceAllStringFunc(text, func(m string) string {
		sub := relRef.FindStringSubmatch(m)
		return fmt.Sprintf("rel(%q, %q)", mapped(sub[1]), mapped(sub[2]))
	})
	text = propRef.ReplaceAllStringFunc(text, func(m string) string {
		sub := propRef.FindStringSubmatch(m)
		return fmt.Sprintf("prop(%q)", mapped(sub[1]))
	})
	return text, firstErr
}
