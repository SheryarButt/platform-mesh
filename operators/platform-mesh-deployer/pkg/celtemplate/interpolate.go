/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package celtemplate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Expressions are enclosed in "${" and "}".
// The extraction and the rewriting of a mixed template into a single CEL concatenation
// follow the approach of kubernetes-sigs/kro's pkg/graph/parser, with two additions:
//   - single-quoted CEL string literals are recognised
//   - "$${" escapes a literal "${" so a payload can carry shell-style variables verbatim
const (
	exprStart = "${"
	escaped   = "$${"
)

// ErrNestedExpression is returned for "${outer(${inner})}".
// Nesting is only allowed inside a string literal, as in `${outer("${inner}")}`.
var ErrNestedExpression = errors.New("nested expressions are not allowed unless inside string literals")

// exprMatch is an expression and its position in the original string.
type exprMatch struct {
	expr  string // without the enclosing ${ }
	start int    // index of "${"
	end   int    // index after the closing "}"
}

// extractExpressions returns every non-nested expression in s.
// Braces are counted so map literals work, and braces inside CEL string literals are ignored.
func extractExpressions(s string) ([]exprMatch, error) {
	var matches []exprMatch

	for start := 0; start < len(s); {
		idx := indexExprStart(s[start:])
		if idx < 0 {
			break
		}
		open := start + idx

		end, err := expressionEnd(s, open)
		if err != nil {
			return nil, err
		}
		if end < 0 {
			// Unbalanced: not an expression, keep scanning after "${".
			start = open + len(exprStart)
			continue
		}

		matches = append(matches, exprMatch{
			expr:  s[open+len(exprStart) : end],
			start: open,
			end:   end + 1,
		})
		start = end + 1
	}
	return matches, nil
}

// indexExprStart finds the next unescaped "${", skipping over "$${".
func indexExprStart(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if strings.HasPrefix(s[i:], escaped) {
			i += len(escaped) - 1
			continue
		}
		if s[i+1] == '{' {
			return i
		}
	}
	return -1
}

// expressionEnd returns the index of the "}" closing the expression that opens at start, or -1 when the braces do not balance.
func expressionEnd(s string, start int) (int, error) {
	depth := 1
	var quote byte
	for i := start + len(exprStart); i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i, nil
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			return 0, ErrNestedExpression
		}
	}
	return -1, nil
}

// buildTemplate rewrites a string holding literals and expressions into a single CEL concatenation, so CEL performs the joining and type checking. "prefix-${expr}" becomes `"prefix-" + (expr)`.
func buildTemplate(s string, matches []exprMatch) string {
	var parts []string
	pos := 0
	for _, m := range matches {
		if m.start > pos {
			parts = append(parts, strconv.Quote(unescape(s[pos:m.start])))
		}
		parts = append(parts, "("+m.expr+")")
		pos = m.end
	}
	if pos < len(s) {
		parts = append(parts, strconv.Quote(unescape(s[pos:])))
	}
	return strings.Join(parts, " + ")
}

// unescape turns "$${" back into a literal "${". A "$$" not followed by "{" is left alone, so shell constructs such as "echo $$" survive.
func unescape(s string) string {
	return strings.ReplaceAll(s, escaped, exprStart)
}

// Interpolate evaluates the ${ ... } expressions in s.
// A string that is exactly one expression yields that expression's native value, so a manifest can carry non-string leaves such as replica counts.
// Anything else is evaluated as a CEL concatenation and yields a string.
func Interpolate(s string, ctx Context) (any, error) {
	matches, err := extractExpressions(s)
	if err != nil {
		return nil, fmt.Errorf("in %q: %w", s, err)
	}
	if len(matches) == 0 {
		return unescape(s), nil
	}
	if len(matches) == 1 && matches[0].start == 0 && matches[0].end == len(s) {
		return evalAny(matches[0].expr, ctx)
	}
	return evalAny(buildTemplate(s, matches), ctx)
}

// InterpolateValue walks a decoded manifest and interpolates every string leaf, including map keys.
func InterpolateValue(v any, ctx Context) (any, error) {
	switch t := v.(type) {
	case string:
		return Interpolate(t, ctx)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			key, err := Interpolate(k, ctx)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k, err)
			}
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("key %q evaluated to %T, want string", k, key)
			}
			nested, err := InterpolateValue(val, ctx)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[name] = nested
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			nested, err := InterpolateValue(val, ctx)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = nested
		}
		return out, nil
	default:
		return v, nil
	}
}
