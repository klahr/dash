package doctor

import (
	"slices"
	"strings"
)

// builtins are shell builtins and keywords: a command starting with one
// needs no tool installed.
var builtins = map[string]bool{
	"!": true, ".": true, ":": true, "[": true, "[[": true, "{": true, "}": true, "alias": true, "bg": true, "bind": true,
	"break": true, "builtin": true, "case": true, "cd": true, "command": true, "continue": true, "declare": true,
	"do": true, "done": true, "echo": true, "elif": true, "else": true, "esac": true, "eval": true, "exec": true,
	"exit": true, "export": true, "false": true, "fg": true, "fi": true, "for": true, "function": true, "getopts": true,
	"hash": true, "if": true, "in": true, "jobs": true, "kill": true, "let": true, "local": true, "popd": true,
	"printf": true, "pushd": true, "pwd": true, "read": true, "readonly": true, "return": true, "select": true,
	"set": true, "shift": true, "source": true, "test": true, "then": true, "time": true, "times": true, "trap": true,
	"true": true, "type": true, "typeset": true, "ulimit": true, "umask": true, "unalias": true, "unset": true,
	"until": true, "wait": true, "while": true,
}

// prefixes run the command after them (and their own flags and, for env,
// assignments): the tool is the word that follows.
var prefixes = map[string]bool{"env": true, "nohup": true, "nice": true, "sudo": true,
	// Keywords a command follows: `if go vet`, `then make`, `! grep`.
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true, "time": true, "{": true}

// CommandNames are the tools a shell command line runs: the first word of
// each simple command in it (split at ;, &&, ||, |, & and new lines), once
// each, in order. Shell builtins and keywords, variable assignments, paths
// (./script, bin/x, /usr/bin/x) and words made by the shell ($x, $(…),
// `…`) are skipped: they need nothing installed, or can't be known. A
// make target is checked as make.
func CommandNames(cmd string) []string {
	var out []string
	for _, simple := range splitCommands(cmd) {
		name := firstWord(simple)
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// splitCommands splits a command line into its simple commands, outside
// quotes. Comments (# to the end of the line) are dropped.
func splitCommands(s string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	flush := func() {
		if t := strings.TrimSpace(b.String()); t != "" {
			out = append(out, t)
		}
		b.Reset()
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(rs) {
				b.WriteRune(c)
				i++
				c = rs[i]
			} else if c == quote {
				quote = 0
			}
			b.WriteRune(c)
		case c == '\'' || c == '"' || c == '`':
			quote = c
			b.WriteRune(c)
		case c == '\\' && i+1 < len(rs):
			if rs[i+1] == '\n' { // a line continued
				i++
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(c)
			i++
			b.WriteRune(rs[i])
		case c == '#' && (i == 0 || isSpace(rs[i-1]) || strings.ContainsRune(";&|(\n", rs[i-1])):
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			flush()
		default:
			b.WriteRune(c)
		}
	}
	flush()
	return out
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

// firstWord is the tool simple runs, or "" when it runs none that can be
// checked.
func firstWord(simple string) string {
	words := strings.Fields(simple)
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case isAssignment(w):
			continue
		case prefixes[w]:
			// Skip its flags: `env -i FOO=1 go`, `nice -n 5 make`.
			for i+1 < len(words) && strings.HasPrefix(words[i+1], "-") {
				i++
				if w == "nice" && words[i] == "-n" && i+1 < len(words) {
					i++
				}
			}
			continue
		}
		w = strings.Trim(w, `"'`)
		if w == "" || builtins[w] || strings.ContainsAny(w, "/\\$`(){}<>*?=~") || strings.HasPrefix(w, "-") {
			return ""
		}
		return w
	}
	return ""
}

// isAssignment reports whether w is a shell variable assignment (FOO=bar).
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
