package commands

import (
	"errors"
	"strings"
	"unicode"
)

// SplitArguments handles literal quoted arguments without evaluating a shell,
// environment variables, command substitutions or globs.
func SplitArguments(input string) ([]string, error) {
	var args []string
	var value strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range input {
		switch {
		case escaped:
			value.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, started = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				value.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			if started {
				args = append(args, value.String())
				value.Reset()
				started = false
			}
		default:
			value.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated quote or escape in command arguments")
	}
	if started {
		args = append(args, value.String())
	}
	return args, nil
}
