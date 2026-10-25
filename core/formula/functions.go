package formula

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/expr-lang/expr"
)

// functions returns the functions formulas can call on top of the expr builtins
// (sum, min, max, mean, count, len, filter, map, join, upper, lower, abs, ...).
// Each one declares its signatures so that expr can type-check calls.
func functions() []expr.Option {
	return []expr.Option{
		expr.DisableBuiltin("round"),
		expr.Function("round", fnRound,
			new(func(float64) float64),
			new(func(float64, int) float64),
		),
		expr.DisableBuiltin("min"),
		expr.Function("min", fnMin,
			new(func([]float64) float64),
			new(func(float64, float64) float64),
		),
		expr.DisableBuiltin("max"),
		expr.Function("max", fnMax,
			new(func([]float64) float64),
			new(func(float64, float64) float64),
		),
		expr.Function("avg", fnAvg,
			new(func([]float64) float64),
		),
		expr.Function("empty", fnEmpty,
			new(func(float64) bool),
			new(func(string) bool),
			new(func(bool) bool),
			new(func(time.Time) bool),
			new(func([]string) bool),
			new(func([]float64) bool),
			new(func([]time.Time) bool),
			new(func([]bool) bool),
		),
		expr.Function("dateAdd", fnDateAdd,
			new(func(time.Time, int, string) time.Time),
			new(func(time.Time, float64, string) time.Time),
		),
		expr.Function("dateBetween", fnDateBetween,
			new(func(time.Time, time.Time, string) float64),
		),
		expr.Function("formatDate", fnFormatDate,
			new(func(time.Time, string) string),
		),
		expr.Function("earliest", fnEarliest,
			new(func([]time.Time) time.Time),
		),
		expr.Function("latest", fnLatest,
			new(func([]time.Time) time.Time),
		),
	}
}

func fnRound(params ...any) (any, error) {
	x := params[0].(float64)
	digits := 0
	if len(params) == 2 {
		digits = params[1].(int)
	}
	pow := math.Pow(10, float64(digits))
	return math.Round(x*pow) / pow, nil
}

func fnMin(params ...any) (any, error) {
	return pickNumber(params, func(a, b float64) bool { return a < b })
}

func fnMax(params ...any) (any, error) {
	return pickNumber(params, func(a, b float64) bool { return a > b })
}

// pickNumber takes either one list of numbers or two numbers.
func pickNumber(params []any, better func(a, b float64) bool) (any, error) {
	list, ok := params[0].([]float64)
	if !ok {
		list = []float64{toFloat(params[0]), toFloat(params[1])}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("empty list of numbers")
	}
	best := list[0]
	for _, v := range list[1:] {
		if better(v, best) {
			best = v
		}
	}
	return best, nil
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	return math.NaN()
}

func fnAvg(params ...any) (any, error) {
	list := params[0].([]float64)
	if len(list) == 0 {
		return nil, fmt.Errorf("avg of an empty list")
	}
	var total float64
	for _, v := range list {
		total += v
	}
	return total / float64(len(list)), nil
}

func fnEmpty(params ...any) (any, error) {
	switch v := params[0].(type) {
	case float64:
		return v == 0, nil
	case string:
		return v == "", nil
	case bool:
		return !v, nil
	case time.Time:
		return v.IsZero(), nil
	case []string:
		return len(v) == 0, nil
	case []float64:
		return len(v) == 0, nil
	case []time.Time:
		return len(v) == 0, nil
	case []bool:
		return len(v) == 0, nil
	}
	return params[0] == nil, nil
}

func fnDateAdd(params ...any) (any, error) {
	t := params[0].(time.Time)
	var n int
	switch v := params[1].(type) {
	case int:
		n = v
	case float64:
		n = int(v)
	}
	switch strings.ToLower(params[2].(string)) {
	case "minutes":
		return t.Add(time.Duration(n) * time.Minute), nil
	case "hours":
		return t.Add(time.Duration(n) * time.Hour), nil
	case "days":
		return t.AddDate(0, 0, n), nil
	case "weeks":
		return t.AddDate(0, 0, 7*n), nil
	case "months":
		return t.AddDate(0, n, 0), nil
	case "years":
		return t.AddDate(n, 0, 0), nil
	}
	return nil, fmt.Errorf("unknown date unit %q, use minutes, hours, days, weeks, months or years", params[2])
}

func fnDateBetween(params ...any) (any, error) {
	a, b := params[0].(time.Time), params[1].(time.Time)
	d := a.Sub(b)
	switch strings.ToLower(params[2].(string)) {
	case "minutes":
		return math.Trunc(d.Minutes()), nil
	case "hours":
		return math.Trunc(d.Hours()), nil
	case "days":
		return math.Trunc(d.Hours() / 24), nil
	case "weeks":
		return math.Trunc(d.Hours() / (24 * 7)), nil
	case "months":
		return float64(monthsBetween(a, b)), nil
	case "years":
		return float64(monthsBetween(a, b) / 12), nil
	}
	return nil, fmt.Errorf("unknown date unit %q, use minutes, hours, days, weeks, months or years", params[2])
}

// monthsBetween counts whole months from b to a (negative when a is before b).
func monthsBetween(a, b time.Time) int {
	sign := 1
	if a.Before(b) {
		a, b = b, a
		sign = -1
	}
	months := (a.Year()-b.Year())*12 + int(a.Month()-b.Month())
	if b.AddDate(0, months, 0).After(a) {
		months--
	}
	return sign * months
}

var dateTokens = strings.NewReplacer(
	"YYYY", "2006",
	"YY", "06",
	"MM", "01",
	"DD", "02",
	"HH", "15",
	"mm", "04",
	"ss", "05",
)

func fnFormatDate(params ...any) (any, error) {
	return params[0].(time.Time).Format(dateTokens.Replace(params[1].(string))), nil
}

func fnEarliest(params ...any) (any, error) {
	return pickTime(params[0].([]time.Time), func(a, b time.Time) bool { return a.Before(b) })
}

func fnLatest(params ...any) (any, error) {
	return pickTime(params[0].([]time.Time), func(a, b time.Time) bool { return a.After(b) })
}

func pickTime(list []time.Time, better func(a, b time.Time) bool) (any, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("empty list of dates")
	}
	best := list[0]
	for _, t := range list[1:] {
		if better(t, best) {
			best = t
		}
	}
	return best, nil
}
