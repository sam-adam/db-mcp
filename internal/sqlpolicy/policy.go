// Package sqlpolicy implements a deliberately small, fail-closed SQL policy.
// It is not a SQL rewriter: anything outside its supported read-only grammar is
// rejected and database read-only roles/transactions remain mandatory.
package sqlpolicy

import (
	"fmt"
	"strings"
	"unicode"
)

var ErrUnsafe = fmt.Errorf("query is outside the supported read-only SQL policy")

var forbidden = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "MERGE": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "GRANT": true,
	"REVOKE": true, "CALL": true, "DO": true, "COPY": true, "LOAD": true, "HANDLER": true,
	"LOCK": true, "UNLOCK": true, "FOR": true, "INTO": true, "OUTFILE": true, "DUMPFILE": true,
	"PROCEDURE": true, "FUNCTION": true, "SET": true, "USE": true, "PREPARE": true, "EXECUTE": true,
}
var safeFunctions = map[string]bool{
	"ABS": true, "AVG": true, "CAST": true, "CEIL": true, "CEILING": true, "COALESCE": true, "CONCAT": true,
	"COUNT": true, "CURRENT_DATE": true, "CURRENT_TIMESTAMP": true, "DATE": true, "EXTRACT": true, "FLOOR": true,
	"GREATEST": true, "LEAST": true, "LENGTH": true, "LOWER": true, "MAX": true, "MIN": true, "NULLIF": true,
	"NOW": true, "ROUND": true, "SUBSTRING": true, "SUM": true, "TRIM": true, "UPPER": true, "VERSION": true,
}

// Validate returns an actionable policy error, never SQL text (which may contain secrets).
func Validate(sql string) error {
	tokens, err := lex(sql)
	if err != nil || len(tokens) == 0 {
		return ErrUnsafe
	}
	for _, t := range tokens {
		if forbidden[t] {
			return ErrUnsafe
		}
	}
	switch tokens[0] {
	case "SELECT":
		return validateSelect(tokens)
	case "SHOW":
		return nil
	case "EXPLAIN":
		for _, t := range tokens {
			if t == "ANALYZE" {
				return ErrUnsafe
			}
		}
		for i, t := range tokens {
			if t == "SELECT" {
				return validateSelect(tokens[i:])
			}
		}
		return ErrUnsafe
	default:
		return ErrUnsafe
	}
}
func validateSelect(tokens []string) error {
	for i, t := range tokens {
		if forbidden[t] || t == "WITH" || t == "ANALYZE" {
			return ErrUnsafe
		}
		if i+1 < len(tokens) && tokens[i+1] == "(" && isIdent(t) && !safeFunctions[t] {
			return ErrUnsafe
		}
	}
	return nil
}
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func lex(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		r := rune(s[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if s[i] == ';' || (s[i] == '-' && i+1 < len(s) && s[i+1] == '-') || (s[i] == '/' && i+1 < len(s) && s[i+1] == '*') {
			return nil, ErrUnsafe
		}
		if s[i] == '\'' {
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, ErrUnsafe
			}
			out = append(out, "LITERAL")
			continue
		}
		if s[i] == '`' || s[i] == '"' {
			quote := s[i]
			i++
			for i < len(s) && s[i] != quote {
				i++
			}
			if i == len(s) {
				return nil, ErrUnsafe
			}
			i++
			out = append(out, "IDENT")
			continue
		}
		if unicode.IsLetter(r) || s[i] == '_' {
			start := i
			i++
			for i < len(s) {
				x := rune(s[i])
				if !unicode.IsLetter(x) && !unicode.IsDigit(x) && s[i] != '_' {
					break
				}
				i++
			}
			out = append(out, strings.ToUpper(s[start:i]))
			continue
		}
		if unicode.IsDigit(r) {
			for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
				i++
			}
			out = append(out, "NUMBER")
			continue
		}
		switch s[i] {
		case '(', ')', ',', '.', '+', '-', '*', '/', '=', '<', '>', '!', '|':
			out = append(out, string(s[i]))
			i++
		default:
			return nil, ErrUnsafe
		}
	}
	return out, nil
}
