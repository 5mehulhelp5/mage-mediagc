// Package phpconfig parses the subset of PHP syntax used by Magento 2's
// app/etc/env.php configuration file, so mage-mediagc can read database
// credentials without requiring any PHP runtime on the host.
//
// Supported syntax:
//
//   - optional `<?php` opening tag
//   - statements preceding `return`, which are skipped
//   - `return <expr>;`
//   - nested arrays written as `[...]` or `array(...)`
//   - `=>` key/value pairs and implicit integer keys
//   - single- and double-quoted strings with PHP escape sequences
//   - integers, floats, booleans, null
//   - `//`, `#` and `/* */` comments
//
// Unsupported PHP constructs (constants, function calls, concatenation,
// heredocs) cause a descriptive error rather than being silently ignored,
// because guessing at credentials is worse than failing loudly.
package phpconfig

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// ParseFile reads and parses a PHP file that returns an array.
func ParseFile(path string) (map[string]any, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(src)
}

// Parse parses PHP source that returns an array at top level.
func Parse(src []byte) (map[string]any, error) {
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	lx := newLexer(src)
	toks, err := lx.tokenize()
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	return p.parseProgram()
}

// ---------------------------------------------------------------------------
// Lexer
// ---------------------------------------------------------------------------

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokOpenTag
	tokString
	tokNumber
	tokIdent
	tokLBracket
	tokRBracket
	tokLParen
	tokRParen
	tokComma
	tokArrow
	tokSemicolon
	tokAssign
)

type token struct {
	kind tokenKind
	text string
	line int
	col  int
}

func (t token) describe() string {
	switch t.kind {
	case tokEOF:
		return "end of file"
	case tokString:
		return fmt.Sprintf("string %q", t.text)
	default:
		return fmt.Sprintf("%q", t.text)
	}
}

type lexer struct {
	src  []byte
	pos  int
	line int
	col  int
}

func newLexer(src []byte) *lexer {
	return &lexer{src: src, line: 1, col: 1}
}

func (l *lexer) errf(format string, args ...any) error {
	return fmt.Errorf("line %d col %d: %s", l.line, l.col, fmt.Sprintf(format, args...))
}

func (l *lexer) atEnd() bool { return l.pos >= len(l.src) }

func (l *lexer) peek() byte {
	if l.atEnd() {
		return 0
	}
	return l.src[l.pos]
}

// peekNext returns the byte after the cursor, or 0 at (or past) the end.
func (l *lexer) peekNext() byte {
	if l.pos+1 >= len(l.src) {
		return 0
	}
	return l.src[l.pos+1]
}

func (l *lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

func (l *lexer) skipLineComment() {
	for !l.atEnd() && l.peek() != '\n' {
		l.advance()
	}
}

func (l *lexer) skipBlockComment() error {
	l.advance() // '/'
	l.advance() // '*'
	for !l.atEnd() {
		if l.peek() == '*' && l.peekNext() == '/' {
			l.advance()
			l.advance()
			return nil
		}
		l.advance()
	}
	return l.errf("unterminated block comment")
}

func (l *lexer) skipSpaceAndComments() error {
	for !l.atEnd() {
		c := l.peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.advance()
		case c == '/' && l.peekNext() == '/':
			l.skipLineComment()
		case c == '#':
			l.skipLineComment()
		case c == '/' && l.peekNext() == '*':
			if err := l.skipBlockComment(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
	return nil
}

// skipOpenTag consumes an optional PHP opening tag along with anything that
// may legally precede it (a shebang line, leading whitespace). Files without
// a tag are accepted as long as the remaining source is an array expression.
//
// The tag itself is `<?php`, `<?=`, or a bare `<?`; the `php` suffix is
// case-insensitive, matching PHP's own rule.
func (l *lexer) skipOpenTag() {
	start := 0
	if bytes.HasPrefix(l.src, []byte("#!")) {
		nl := bytes.IndexByte(l.src, '\n')
		if nl < 0 {
			return // a shebang with no newline: nothing to parse
		}
		start = nl + 1
	}
	for start < len(l.src) && isSpace(l.src[start]) {
		start++
	}
	if !bytes.HasPrefix(l.src[start:], []byte("<?")) {
		return
	}
	for l.pos < start {
		l.advance()
	}
	l.advance() // '<'
	l.advance() // '?'
	if l.hasPrefixFold("php") {
		l.advance()
		l.advance()
		l.advance()
	}
}

func (l *lexer) hasPrefixFold(s string) bool {
	if l.pos+len(s) > len(l.src) {
		return false
	}
	return strings.EqualFold(string(l.src[l.pos:l.pos+len(s)]), s)
}

func (l *lexer) tokenize() ([]token, error) {
	var toks []token
	l.skipOpenTag()
	for {
		if err := l.skipSpaceAndComments(); err != nil {
			return nil, err
		}
		if l.atEnd() {
			toks = append(toks, token{kind: tokEOF, line: l.line, col: l.col})
			return toks, nil
		}
		startLine, startCol := l.line, l.col
		c := l.peek()

		switch {
		case c == '[':
			l.advance()
			toks = append(toks, token{kind: tokLBracket, text: "[", line: startLine, col: startCol})
		case c == ']':
			l.advance()
			toks = append(toks, token{kind: tokRBracket, text: "]", line: startLine, col: startCol})
		case c == '(':
			l.advance()
			toks = append(toks, token{kind: tokLParen, text: "(", line: startLine, col: startCol})
		case c == ')':
			l.advance()
			toks = append(toks, token{kind: tokRParen, text: ")", line: startLine, col: startCol})
		case c == ',':
			l.advance()
			toks = append(toks, token{kind: tokComma, text: ",", line: startLine, col: startCol})
		case c == ';':
			l.advance()
			toks = append(toks, token{kind: tokSemicolon, text: ";", line: startLine, col: startCol})
		case c == '=' && l.peekNext() == '>':
			l.advance()
			l.advance()
			toks = append(toks, token{kind: tokArrow, text: "=>", line: startLine, col: startCol})
		case c == '=':
			l.advance()
			toks = append(toks, token{kind: tokAssign, text: "=", line: startLine, col: startCol})
		case c == '$':
			// A variable, which only ever appears in a leading statement that
			// the parser skips. Reading it as one token keeps the lexer from
			// choking on `$ignored = 'x';`.
			l.advance()
			toks = append(toks, token{kind: tokIdent, text: "$" + l.readIdent(), line: startLine, col: startCol})
		case c == '\'' || c == '"':
			s, err := l.readString(c)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tokString, text: s, line: startLine, col: startCol})
		case c == '-' && isDigit(l.peekNext()):
			l.advance()
			num, err := l.readNumber(startLine, startCol, true)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tokNumber, text: num, line: startLine, col: startCol})
		case isDigit(c):
			num, err := l.readNumber(startLine, startCol, false)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tokNumber, text: num, line: startLine, col: startCol})
		case isIdentStart(c):
			id := l.readIdent()
			toks = append(toks, token{kind: tokIdent, text: id, line: startLine, col: startCol})
		default:
			return nil, l.errf("unexpected character %q", string(c))
		}
	}
}

func (l *lexer) readString(quote byte) (string, error) {
	l.advance() // opening quote
	var sb strings.Builder
	for {
		if l.atEnd() {
			return "", l.errf("unterminated string literal")
		}
		c := l.advance()
		if c == quote {
			return sb.String(), nil
		}
		if c == '\\' && !l.atEnd() {
			esc := l.advance()
			if quote == '\'' {
				// In single-quoted strings PHP only honors \\ and \'
				switch esc {
				case '\\', '\'':
					sb.WriteByte(esc)
				default:
					sb.WriteByte('\\')
					sb.WriteByte(esc)
				}
				continue
			}
			switch esc {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '0':
				sb.WriteByte(0)
			case '"', '\\', '$':
				sb.WriteByte(esc)
			default:
				sb.WriteByte('\\')
				sb.WriteByte(esc)
			}
			continue
		}
		sb.WriteByte(c)
	}
}

func (l *lexer) readNumber(line, col int, negative bool) (string, error) {
	var sb strings.Builder
	if negative {
		sb.WriteByte('-')
	}
	seenDot := false
	seenExp := false
	for !l.atEnd() {
		c := l.peek()
		switch {
		case isDigit(c):
			sb.WriteByte(l.advance())
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
			sb.WriteByte(l.advance())
		case (c == 'e' || c == 'E') && !seenExp:
			seenExp = true
			sb.WriteByte(l.advance())
			if l.peek() == '+' || l.peek() == '-' {
				sb.WriteByte(l.advance())
			}
		default:
			return sb.String(), nil
		}
	}
	if sb.String() == "-" {
		return "", fmt.Errorf("line %d col %d: malformed number", line, col)
	}
	return sb.String(), nil
}

func (l *lexer) readIdent() string {
	var sb strings.Builder
	for !l.atEnd() {
		c := l.peek()
		if isIdentStart(c) || isDigit(c) {
			sb.WriteByte(l.advance())
			continue
		}
		// Namespaced constant such as Magento\Framework\App\Cache::class would
		// also produce backslashes; keep them so the parser can report clearly.
		if c == '\\' {
			sb.WriteByte(l.advance())
			continue
		}
		break
	}
	return sb.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

func isIdentStart(c byte) bool {
	return c == '_' || unicode.IsLetter(rune(c))
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

type entry struct {
	key   any
	value any
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) cur() token { return p.toks[p.pos] }
func (p *parser) next()      { p.pos++ }

func (p *parser) errf(t token, msg string) error {
	return fmt.Errorf("line %d col %d: %s (got %s)", t.line, t.col, msg, t.describe())
}

func (p *parser) parseProgram() (map[string]any, error) {
	// A stock env.php is a single `return [...]`, but hand-edited files
	// sometimes carry statements before it. Skip anything ahead of the
	// `return` instead of failing on PHP the tool has no business executing.
	for {
		t := p.cur()
		if t.kind == tokEOF {
			return nil, fmt.Errorf("line %d col %d: no `return` statement found", t.line, t.col)
		}
		if t.kind == tokIdent && strings.EqualFold(t.text, "return") {
			p.next()
			break
		}
		if err := p.skipStatement(); err != nil {
			return nil, err
		}
	}

	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top-level value must be an associative array, got %T", v)
	}
	return m, nil
}

// skipStatement discards tokens up to and including the next top-level
// semicolon. It stops early when it meets a `return`, leaving that token for
// the caller so nested arrays inside the skipped statement cannot confuse it.
func (p *parser) skipStatement() error {
	depth := 0
	for {
		t := p.cur()
		switch t.kind {
		case tokEOF:
			return fmt.Errorf("line %d col %d: expected a `return` statement", t.line, t.col)
		case tokLBracket, tokLParen:
			depth++
		case tokRBracket, tokRParen:
			if depth > 0 {
				depth--
			}
		case tokSemicolon:
			p.next()
			if depth == 0 {
				return nil
			}
			continue
		case tokIdent:
			if depth == 0 && strings.EqualFold(t.text, "return") {
				return nil
			}
		}
		p.next()
	}
}

func (p *parser) parseExpr() (any, error) {
	t := p.cur()
	switch t.kind {
	case tokString:
		p.next()
		return t.text, nil
	case tokNumber:
		p.next()
		if strings.ContainsAny(t.text, ".eE") {
			f, err := strconv.ParseFloat(t.text, 64)
			if err != nil {
				return nil, p.errf(t, "invalid float")
			}
			return f, nil
		}
		i, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, p.errf(t, "invalid integer")
		}
		return i, nil
	case tokIdent:
		switch strings.ToLower(t.text) {
		case "true":
			p.next()
			return true, nil
		case "false":
			p.next()
			return false, nil
		case "null":
			p.next()
			return nil, nil
		case "array":
			p.next()
			return p.parseArrayBody(tokLParen, tokRParen)
		}
		return nil, p.errf(t, "unsupported PHP expression "+
			"(constants, function calls and concatenation are not supported)")
	case tokLBracket:
		return p.parseArrayBody(tokLBracket, tokRBracket)
	default:
		return nil, p.errf(t, "expected a value")
	}
}

func (p *parser) parseArrayBody(open, close tokenKind) (any, error) {
	openTok := p.cur()
	if openTok.kind != open {
		return nil, p.errf(openTok, "expected array opener")
	}
	p.next()

	var entries []entry
	closed := false
	for !closed {
		t := p.cur()
		if t.kind == close {
			p.next()
			break
		}
		if t.kind == tokEOF {
			return nil, p.errf(t, "unterminated array")
		}
		key, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.cur().kind == tokArrow {
			p.next()
			val, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry{key: key, value: val})
		} else {
			entries = append(entries, entry{key: nil, value: key})
		}
		switch p.cur().kind {
		case tokComma:
			p.next()
		case close:
			closed = true
			p.next()
		default:
			return nil, p.errf(p.cur(), "expected \",\" or array terminator")
		}
	}
	return buildValue(entries), nil
}

// buildValue converts parsed entries into either []any (pure list with
// implicit keys) or map[string]any (anything carrying explicit keys).
func buildValue(entries []entry) any {
	hasExplicitKey := false
	for _, e := range entries {
		if e.key != nil {
			hasExplicitKey = true
			break
		}
	}
	if !hasExplicitKey {
		out := make([]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.value)
		}
		return out
	}
	m := make(map[string]any, len(entries))
	auto := int64(0)
	for _, e := range entries {
		var k string
		switch kk := e.key.(type) {
		case nil:
			k = strconv.FormatInt(auto, 10)
			auto++
		case string:
			k = kk
		case int64:
			k = strconv.FormatInt(kk, 10)
			if kk >= auto {
				auto = kk + 1
			}
		default:
			k = fmt.Sprint(kk)
		}
		if _, dup := m[k]; dup {
			// PHP keeps the last assignment; mirror that.
			delete(m, k)
		}
		m[k] = e.value
	}
	return m
}
