package parser

import (
	"github.com/jhnl/dingo/common"
	"github.com/jhnl/dingo/token"
)

type lexer struct {
	src        []byte
	filename   string
	errors     *common.ErrorList
	ch         rune
	chOffset   int
	lineOffset int
	lineCount  int
	readOffset int
	prev       token.ID
}

func (l *lexer) init(src []byte, filename string, errors *common.ErrorList) {
	l.src = src
	l.filename = filename
	l.errors = errors
	l.ch = ' '
	l.chOffset = 0
	l.readOffset = 0
	l.lineOffset = 0
	l.lineCount = 1
	l.prev = token.Invalid
	l.next()
}

func (l *lexer) lex() token.Token {
	l.skipWhitespace(false)

	startOffset := 0
	tok := token.Token{
		Literal: "",
		ID:      token.Invalid,
	}

	// Insert semicolon if needed

	if l.ch == '\n' {
		if isLineTerminator(l.prev) {
			tok.ID = token.Semicolon
			tok.Pos = l.newPos()
			startOffset = l.chOffset
			l.next()
			tok.Literal = string(l.src[startOffset:l.chOffset])
			l.prev = tok.ID
			return tok
		}

		l.skipWhitespace(true)
	}

	pos := l.newPos()
	tok.Pos = pos
	startOffset = l.chOffset

	switch ch1 := l.ch; {
	case isLetter(ch1):
		tok.ID, tok.Literal = l.lexIdent()
	case isDigit(ch1, 10):
		tok.ID = l.lexNumber(false, pos)
	case ch1 == '"':
		l.lexString(pos)
		tok.ID = token.String
	case ch1 == '.':
		l.next()
		if isDigit(l.ch, 10) {
			tok.ID = l.lexNumber(true, pos)
		} else {
			tok.ID = token.Dot
		}
	default:
		l.next()

		switch ch1 {
		case -1:
			if isLineTerminator(l.prev) {
				tok.ID = token.Semicolon
				tok.Literal = "\n"
			} else {
				tok.ID = token.EOF
			}
		case '(':
			tok.ID = token.Lparen
		case ')':
			tok.ID = token.Rparen
		case '{':
			tok.ID = token.Lbrace
		case '}':
			tok.ID = token.Rbrace
		case '[':
			tok.ID = token.Lbrack
		case ']':
			tok.ID = token.Rbrack
		case ',':
			tok.ID = token.Comma
		case ';':
			tok.ID = token.Semicolon
		case ':':
			tok.ID = token.Colon
		case '+':
			tok.ID = l.lexOptionalEqual(token.AddAssign, token.Add)
		case '-':
			tok.ID = l.lexOptionalEqual(token.SubAssign, token.Sub)
		case '*':
			tok.ID = l.lexOptionalEqual(token.MulAssign, token.Mul)
		case '/':
			if l.ch == '/' {
				// Single-line comment
				l.next()
				for l.ch != '\n' && l.ch != -1 {
					l.next()
				}
				l.next()
				tok.ID = token.Comment
			} else if l.ch == '*' {
				// Multi-line comment
				l.next()
				nested := 1
				for l.ch != -1 && nested > 0 {
					ch := l.ch
					l.next()
					if ch == '*' && l.ch == '/' {
						nested--
						l.next()
					} else if ch == '/' && l.ch == '*' {
						nested++
						l.next()
					}
				}
				if nested > 0 {
					l.error(pos, "multi-line comment not closed")
				}
				tok.ID = token.MultiComment
			} else {
				tok.ID = l.lexOptionalEqual(token.DivAssign, token.Div)
			}
		case '%':
			tok.ID = l.lexOptionalEqual(token.ModAssign, token.Mod)
		case '=':
			tok.ID = l.lexOptionalEqual(token.Eq, token.Assign)
		case '&':
			tok.ID = l.lexOptional('&', token.Land, token.And)
		case '|':
			tok.ID = l.lexOptional('|', token.Lor, token.Or)
		case '!':
			tok.ID = l.lexOptionalEqual(token.Neq, token.Lnot)
		case '>':
			tok.ID = l.lexOptionalEqual(token.GtEq, token.Gt)
		case '<':
			tok.ID = l.lexOptionalEqual(token.LtEq, token.Lt)
		default:
			tok.ID = token.Invalid
		}
	}

	if len(tok.Literal) == 0 {
		tok.Literal = string(l.src[startOffset:l.chOffset])
	}

	l.prev = tok.ID
	return tok
}

func isLineTerminator(id token.ID) bool {
	switch id {
	case token.Ident,
		token.Integer, token.Float, token.String, token.True, token.False, token.Null,
		token.Rparen, token.Rbrace, token.Rbrack,
		token.Continue, token.Break, token.Return:
		return true
	default:
		return false
	}
}

func isLetter(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isDigit(ch rune, base int) bool {
	base10 := '0' <= ch && ch <= '9'
	switch base {
	case 8:
		return '0' <= ch && ch <= '7'
	case 10:
		return base10
	case 16:
		switch {
		case base10:
			return true
		case 'a' <= ch && ch <= 'f':
			return true
		case 'A' <= ch && ch <= 'F':
			return true
		}
	}

	return false
}

// Only supports ASCII characters for now
//
func (l *lexer) next() {
	if l.readOffset < len(l.src) {
		l.chOffset = l.readOffset
		l.readOffset++
		l.ch = rune(l.src[l.chOffset])
		if l.ch == '\n' {
			l.lineOffset = l.chOffset
			l.lineCount++
		}
	} else {
		l.chOffset = l.readOffset
		l.ch = -1
	}
}

func (l *lexer) newPos() token.Position {
	col := l.chOffset - l.lineOffset
	if col <= 0 {
		col = 1
	}
	return token.Position{Line: l.lineCount, Column: col}
}

func (l *lexer) error(pos token.Position, msg string) {
	l.errors.Add(l.filename, pos, common.SyntaxError, msg)
}

func (l *lexer) skipWhitespace(newline bool) {
	for l.ch == ' ' || l.ch == '\t' || (newline && l.ch == '\n') {
		l.next()
	}
}

func (l *lexer) lexOptional(ch rune, tok0 token.ID, tok1 token.ID) token.ID {
	if l.ch == ch {
		l.next()
		return tok0
	}
	return tok1
}

func (l *lexer) lexOptionalEqual(tok0 token.ID, tok1 token.ID) token.ID {
	return l.lexOptional('=', tok0, tok1)
}

func (l *lexer) lexIdent() (token.ID, string) {
	startOffset := l.chOffset

	for isLetter(l.ch) || isDigit(l.ch, 10) {
		l.next()
	}

	tok := token.Ident
	lit := string(l.src[startOffset:l.chOffset])
	if len(lit) > 1 {
		tok = token.Lookup(lit)
	}

	return tok, lit
}

func (l *lexer) lexDigits(base int) int {
	count := 0
	for {
		if isDigit(l.ch, base) {
			count++
		} else if l.ch != '_' {
			break
		}
		l.next()
	}
	return count
}

func isFractionOrExponent(ch rune) bool {
	return ch == '.' || ch == 'e' || ch == 'E'
}

func (l *lexer) lexNumber(float bool, pos token.Position) token.ID {
	// Floating numbers can only be interpreted in base 10.

	id := token.Integer
	if float {
		id = token.Float
		l.lexDigits(10)
	} else {
		if l.ch == '0' {
			l.next()

			if l.ch == 'x' || l.ch == 'X' {
				// Hex
				l.next()
				if l.lexDigits(16) == 0 {
					l.error(pos, "invalid hexadecimal literal")
				} else if isFractionOrExponent(l.ch) {
					l.error(pos, "hexadecimal float literal is not supported")
				}
			} else {
				// Octal
				l.lexDigits(8)
				if isDigit(l.ch, 10) {
					l.error(pos, "invalid octal literal")
				} else if isFractionOrExponent(l.ch) {
					l.error(pos, "octal float literal is not supported")
				}
			}

			return id
		}

		l.lexDigits(10)

		if l.ch == '.' {
			id = token.Float
			l.next()

			if l.ch == '_' {
				l.error(pos, "decimal point '.' in float literal can not be followed by '_'")
				return id
			}

			l.lexDigits(10)
		}
	}

	if l.ch == 'e' || l.ch == 'E' {
		id = token.Float

		l.next()
		if l.ch == '+' || l.ch == '-' {
			l.next()
		}

		if isDigit(l.ch, 10) {
			l.lexDigits(10)
		} else {
			l.error(pos, "invalid exponent in float literal")
		}
	}

	return id
}

func (l *lexer) lexString(pos token.Position) {
	l.next()

	for {
		ch := l.ch

		if ch == '\n' {
			l.error(pos, "string literal not terminated")
			break
		}

		l.next()
		if ch == '"' {
			break
		}
		if ch == '\\' {
			l.next() // TODO: handle multi-char escape
		}
	}
}
