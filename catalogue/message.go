package catalogue

import "strings"

// MessageArguments returns the argument names an ICU MessageFormat template
// refers to.
//
// This is an argument check, not a renderer: it finds what a template names so
// that a catalogue cannot promise a sentence the record cannot fill, and leaves
// rendering to the viewer, which has a real ICU implementation.
//
// It understands the parts of the grammar that decide whether a brace is an
// argument: quoting with an apostrophe, and the submessages of plural, select,
// selectordinal and choice, whose keywords are not arguments but whose bodies
// may contain some.
func MessageArguments(template string) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	scanMessage([]rune(template), add)
	return out
}

// scanMessage walks message text and hands every argument it meets to add.
func scanMessage(s []rune, add func(string)) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			// A doubled apostrophe is a literal one; otherwise everything up to
			// the next apostrophe is quoted and means nothing to the grammar.
			if i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			i++
			for i < len(s) && s[i] != '\'' {
				i++
			}
		case '{':
			end, ok := matchBrace(s, i)
			if !ok {
				return
			}
			scanArgument(s[i+1:end], add)
			i = end
		}
	}
}

// scanArgument reads the inside of one {...}: a name, optionally a type, and
// optionally a body whose meaning depends on the type.
func scanArgument(s []rune, add func(string)) {
	name, rest, hasType := cutRune(s, ',')
	add(string(name))
	if !hasType {
		return
	}
	kind, body, hasBody := cutRune(rest, ',')
	if !hasBody {
		return
	}
	switch strings.TrimSpace(string(kind)) {
	case "plural", "selectordinal", "select", "choice":
		// The body is keyword {submessage} pairs. The keywords are not
		// arguments; the submessages are messages in their own right.
		for i := 0; i < len(body); i++ {
			if body[i] != '{' {
				continue
			}
			end, ok := matchBrace(body, i)
			if !ok {
				return
			}
			scanMessage(body[i+1:end], add)
			i = end
		}
	default:
		// number, date, time and custom formats: the body is style text.
	}
}

// matchBrace returns the index of the brace closing the one at open.
func matchBrace(s []rune, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			i++
			for i < len(s) && s[i] != '\'' {
				i++
			}
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// cutRune splits at the first occurrence of sep outside any nested braces.
func cutRune(s []rune, sep rune) (before, after []rune, found bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case sep:
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return s, nil, false
}
