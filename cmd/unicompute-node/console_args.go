package main

import "strings"

// splitConsoleArgs supports single- and double-quoted arguments, so source and
// stdin file paths may contain spaces. It intentionally does not execute shell
// expansions; this is only a parser for the UniCompute interactive CLI.
func splitConsoleArgs(line string) []string {
	var args []string
	var current strings.Builder
	var quote rune
	inToken := false

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				inToken = true
				continue
			}
			// Allow an escaped matching quote without stripping Windows path
			// separators such as C:\Users\name.
			if r == '\\' && i+1 < len(runes) && runes[i+1] == quote {
				current.WriteRune(quote)
				i++
				inToken = true
				continue
			}
			current.WriteRune(r)
			inToken = true
			continue
		}

		switch r {
		case '\'', '"':
			quote = r
			inToken = true
		case ' ', '\t', '\r', '\n':
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
		default:
			current.WriteRune(r)
			inToken = true
		}
	}
	if inToken {
		args = append(args, current.String())
	}
	return args
}
