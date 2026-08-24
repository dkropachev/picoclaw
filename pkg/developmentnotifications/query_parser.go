package developmentnotifications

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidQuery = errors.New("invalid development notification query")

var relativeTimePattern = regexp.MustCompile(`^-[1-9][0-9]*(m|h|d|w)$`)

// QueryError carries the zero-based byte position used by an advanced query
// editor to highlight malformed input.
type QueryError struct {
	Position int
	Message  string
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("%v at byte %d: %s", ErrInvalidQuery, e.Position, e.Message)
}

func (e *QueryError) Unwrap() error { return ErrInvalidQuery }

// ParseQuery parses bounded JQL-like notification syntax into a typed AST.
func ParseQuery(input string) (Query, error) {
	if !utf8.ValidString(input) {
		return Query{}, queryError(0, "query is not valid UTF-8")
	}
	if len(input) > MaxQueryBytes {
		return Query{}, queryError(MaxQueryBytes, fmt.Sprintf("query exceeds %d bytes", MaxQueryBytes))
	}
	tokens, err := lexQuery(input)
	if err != nil {
		return Query{}, err
	}
	parser := queryParser{tokens: tokens, inputBytes: len(input)}
	query, err := parser.parse()
	if err != nil {
		return Query{}, err
	}
	if err := query.Validate(); err != nil {
		return Query{}, queryError(len(input), err.Error())
	}
	return query, nil
}

type queryTokenKind uint8

const (
	tokenEOF queryTokenKind = iota
	tokenWord
	tokenString
	tokenEqual
	tokenNotEqual
	tokenContains
	tokenNotContains
	tokenGreater
	tokenGreaterEq
	tokenLess
	tokenLessEq
	tokenLeftParen
	tokenRightParen
	tokenComma
)

type queryToken struct {
	kind queryTokenKind
	text string
	pos  int
}

func lexQuery(input string) ([]queryToken, error) {
	tokens := make([]queryToken, 0, len(input)/4+1)
	for offset := 0; offset < len(input); {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
			continue
		case '(':
			tokens = append(tokens, queryToken{kind: tokenLeftParen, text: "(", pos: offset})
			offset++
		case ')':
			tokens = append(tokens, queryToken{kind: tokenRightParen, text: ")", pos: offset})
			offset++
		case ',':
			tokens = append(tokens, queryToken{kind: tokenComma, text: ",", pos: offset})
			offset++
		case '=':
			tokens = append(tokens, queryToken{kind: tokenEqual, text: "=", pos: offset})
			offset++
		case '~':
			tokens = append(tokens, queryToken{kind: tokenContains, text: "~", pos: offset})
			offset++
		case '!':
			start := offset
			offset++
			if offset >= len(input) {
				return nil, queryError(start, "expected = or ~ after !")
			}
			switch input[offset] {
			case '=':
				tokens = append(tokens, queryToken{kind: tokenNotEqual, text: "!=", pos: start})
			case '~':
				tokens = append(tokens, queryToken{kind: tokenNotContains, text: "!~", pos: start})
			default:
				return nil, queryError(start, "expected = or ~ after !")
			}
			offset++
		case '>', '<':
			start := offset
			character := input[offset]
			offset++
			equal := offset < len(input) && input[offset] == '='
			if equal {
				offset++
			}
			kind := tokenGreater
			if character == '<' {
				kind = tokenLess
			}
			if equal && character == '>' {
				kind = tokenGreaterEq
			} else if equal {
				kind = tokenLessEq
			}
			tokens = append(tokens, queryToken{kind: kind, text: input[start:offset], pos: start})
		case '\'', '"':
			token, next, err := lexQuoted(input, offset)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
			offset = next
		default:
			start := offset
			for offset < len(input) && !queryDelimiter(input[offset]) {
				offset++
			}
			if start == offset {
				return nil, queryError(start, "unexpected character")
			}
			tokens = append(tokens, queryToken{kind: tokenWord, text: input[start:offset], pos: start})
		}
	}
	tokens = append(tokens, queryToken{kind: tokenEOF, pos: len(input)})
	return tokens, nil
}

func queryDelimiter(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '(', ')', ',', '=', '!', '~', '>', '<', '\'', '"':
		return true
	default:
		return false
	}
}

func lexQuoted(input string, start int) (queryToken, int, error) {
	quote := input[start]
	var value strings.Builder
	for offset := start + 1; offset < len(input); offset++ {
		character := input[offset]
		if character == quote {
			return queryToken{kind: tokenString, text: value.String(), pos: start}, offset + 1, nil
		}
		if character != '\\' {
			value.WriteByte(character)
			continue
		}
		offset++
		if offset >= len(input) {
			return queryToken{}, 0, queryError(start, "unterminated quoted string")
		}
		switch input[offset] {
		case '\\':
			value.WriteByte('\\')
		case '\'', '"':
			value.WriteByte(input[offset])
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		default:
			return queryToken{}, 0, queryError(offset-1, "unsupported string escape")
		}
	}
	return queryToken{}, 0, queryError(start, "unterminated quoted string")
}

type queryParser struct {
	tokens     []queryToken
	index      int
	inputBytes int
	depth      int
	predicates int
}

func (p *queryParser) parse() (Query, error) {
	var query Query
	var err error
	if !p.atEnd() && !p.startsOrderBy() {
		query.Filter, err = p.parseOr()
		if err != nil {
			return Query{}, err
		}
	}
	if p.startsOrderBy() {
		query.Order, err = p.parseOrderBy()
		if err != nil {
			return Query{}, err
		}
	}
	if !p.atEnd() {
		return Query{}, p.errorAt(p.peek(), "unexpected token "+strconv.Quote(p.peek().text))
	}
	return query, nil
}

func (p *queryParser) parseOr() (Expression, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.matchWord("OR") {
		right, parseErr := p.parseAnd()
		if parseErr != nil {
			return nil, parseErr
		}
		left = LogicalExpression{Operator: LogicalOr, Left: left, Right: right}
	}
	return left, nil
}

func (p *queryParser) parseAnd() (Expression, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.matchWord("AND") {
		right, parseErr := p.parseNot()
		if parseErr != nil {
			return nil, parseErr
		}
		left = LogicalExpression{Operator: LogicalAnd, Left: left, Right: right}
	}
	return left, nil
}

func (p *queryParser) parseNot() (Expression, error) {
	if p.matchWord("NOT") {
		if p.depth >= MaxQueryDepth {
			return nil, p.errorAt(p.previous(), fmt.Sprintf("nesting exceeds %d", MaxQueryDepth))
		}
		p.depth++
		expression, err := p.parseNot()
		p.depth--
		if err != nil {
			return nil, err
		}
		return Negation{Expression: expression}, nil
	}
	return p.parsePrimary()
}

func (p *queryParser) parsePrimary() (Expression, error) {
	if p.match(tokenLeftParen) {
		open := p.previous()
		if p.depth >= MaxQueryDepth {
			return nil, p.errorAt(open, fmt.Sprintf("nesting exceeds %d", MaxQueryDepth))
		}
		p.depth++
		expression, err := p.parseOr()
		p.depth--
		if err != nil {
			return nil, err
		}
		if !p.match(tokenRightParen) {
			return nil, p.errorAt(p.peek(), "expected )")
		}
		return expression, nil
	}
	return p.parsePredicate()
}

func (p *queryParser) parsePredicate() (Expression, error) {
	fieldToken := p.peek()
	if !p.match(tokenWord) {
		return nil, p.errorAt(fieldToken, "expected query field")
	}
	field, ok := parseField(fieldToken.text)
	if !ok {
		return nil, p.errorAt(fieldToken, "unknown query field "+strconv.Quote(fieldToken.text))
	}
	p.predicates++
	if p.predicates > MaxQueryPredicates {
		return nil, p.errorAt(fieldToken, fmt.Sprintf("query exceeds %d predicates", MaxQueryPredicates))
	}

	operator, err := p.parseOperator()
	if err != nil {
		return nil, err
	}
	predicate := Predicate{Field: field, Operator: operator}
	if operator == OperatorIn || operator == OperatorNotIn {
		if !p.match(tokenLeftParen) {
			return nil, p.errorAt(p.peek(), "expected ( after "+string(operator))
		}
		for {
			if len(predicate.Values) >= MaxQueryINValues {
				return nil, p.errorAt(p.peek(), fmt.Sprintf("IN exceeds %d values", MaxQueryINValues))
			}
			value, valueErr := p.parseValue(field)
			if valueErr != nil {
				return nil, valueErr
			}
			predicate.Values = append(predicate.Values, value)
			if !p.match(tokenComma) {
				break
			}
		}
		if !p.match(tokenRightParen) {
			return nil, p.errorAt(p.peek(), "expected ) after IN values")
		}
	} else {
		value, valueErr := p.parseValue(field)
		if valueErr != nil {
			return nil, valueErr
		}
		predicate.Values = []Value{value}
	}
	if err := validatePredicate(predicate); err != nil {
		return nil, p.errorAt(fieldToken, err.Error())
	}
	return predicate, nil
}

func (p *queryParser) parseOperator() (Operator, error) {
	token := p.peek()
	p.index++
	switch token.kind {
	case tokenEqual:
		return OperatorEqual, nil
	case tokenNotEqual:
		return OperatorNotEqual, nil
	case tokenContains:
		return OperatorContains, nil
	case tokenNotContains:
		return OperatorNotContains, nil
	case tokenGreater:
		return OperatorGreater, nil
	case tokenGreaterEq:
		return OperatorGreaterEq, nil
	case tokenLess:
		return OperatorLess, nil
	case tokenLessEq:
		return OperatorLessEq, nil
	case tokenWord:
		if strings.EqualFold(token.text, "IN") {
			return OperatorIn, nil
		}
		if strings.EqualFold(token.text, "NOT") && p.matchWord("IN") {
			return OperatorNotIn, nil
		}
	}
	if token.kind == tokenEOF {
		p.index--
	}
	return "", p.errorAt(token, "expected query operator")
}

func (p *queryParser) parseValue(field Field) (Value, error) {
	token := p.peek()
	if token.kind != tokenWord && token.kind != tokenString {
		return Value{}, p.errorAt(token, "expected query value")
	}
	p.index++
	raw := token.text
	switch fieldTypeOf(field) {
	case fieldTypeBool:
		if strings.EqualFold(raw, "true") {
			return Value{Kind: ValueBool, Bool: true}, nil
		}
		if strings.EqualFold(raw, "false") {
			return Value{Kind: ValueBool}, nil
		}
		return Value{}, p.errorAt(token, "expected true or false")
	case fieldTypeTime:
		return parseTimeValue(raw, token.pos)
	default:
		// String comparisons and contains operations are case-insensitive. Store
		// lower-case AST values so semantically equivalent queries bind the same
		// cursor even when users vary casing.
		raw = strings.ToLower(raw)
		return Value{Kind: ValueString, Text: raw}, nil
	}
}

func parseTimeValue(raw string, position int) (Value, error) {
	if relativeTimePattern.MatchString(strings.ToLower(raw)) {
		unit := raw[len(raw)-1]
		amount, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
		if err != nil {
			return Value{}, queryError(position, "relative date is too large")
		}
		multiplier := time.Minute
		switch unit {
		case 'h', 'H':
			multiplier = time.Hour
		case 'd', 'D':
			multiplier = 24 * time.Hour
		case 'w', 'W':
			multiplier = 7 * 24 * time.Hour
		}
		if amount > int64((1<<63-1)/multiplier) {
			return Value{}, queryError(position, "relative date is too large")
		}
		offset := -time.Duration(amount) * multiplier
		return Value{Kind: ValueRelativeTime, Text: strings.ToLower(raw), TimeOffset: offset}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		if date, dateErr := time.Parse("2006-01-02", raw); dateErr == nil {
			parsed = date
		} else {
			return Value{}, queryError(position, "expected ISO timestamp or relative date")
		}
	}
	return Value{Kind: ValueTime, Time: parsed.UTC()}, nil
}

func (p *queryParser) parseOrderBy() ([]SortField, error) {
	p.index += 2 // ORDER BY, already verified by startsOrderBy.
	order := make([]SortField, 0, MaxQuerySortFields)
	seen := make(map[Field]struct{}, MaxQuerySortFields)
	for {
		if len(order) >= MaxQuerySortFields {
			return nil, p.errorAt(p.peek(), fmt.Sprintf("ORDER BY exceeds %d fields", MaxQuerySortFields))
		}
		fieldToken := p.peek()
		if !p.match(tokenWord) {
			return nil, p.errorAt(fieldToken, "expected ORDER BY field")
		}
		field, ok := parseField(fieldToken.text)
		if !ok || !validSortField(field) {
			return nil, p.errorAt(fieldToken, "field cannot be sorted")
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, p.errorAt(fieldToken, "duplicate ORDER BY field")
		}
		seen[field] = struct{}{}
		directionToken := p.peek()
		if !p.match(tokenWord) {
			return nil, p.errorAt(directionToken, "expected ASC or DESC")
		}
		direction := Direction(strings.ToUpper(directionToken.text))
		if direction != Ascending && direction != Descending {
			return nil, p.errorAt(directionToken, "expected ASC or DESC")
		}
		order = append(order, SortField{Field: field, Direction: direction})
		if !p.match(tokenComma) {
			break
		}
	}
	return order, nil
}

func (p *queryParser) startsOrderBy() bool {
	return p.index+1 < len(p.tokens) && p.tokens[p.index].kind == tokenWord &&
		strings.EqualFold(p.tokens[p.index].text, "ORDER") && p.tokens[p.index+1].kind == tokenWord &&
		strings.EqualFold(p.tokens[p.index+1].text, "BY")
}

func (p *queryParser) atEnd() bool { return p.peek().kind == tokenEOF }

func (p *queryParser) peek() queryToken { return p.tokens[p.index] }

func (p *queryParser) previous() queryToken { return p.tokens[p.index-1] }

func (p *queryParser) match(kind queryTokenKind) bool {
	if p.peek().kind != kind {
		return false
	}
	p.index++
	return true
}

func (p *queryParser) matchWord(word string) bool {
	if p.peek().kind != tokenWord || !strings.EqualFold(p.peek().text, word) {
		return false
	}
	p.index++
	return true
}

func (p *queryParser) errorAt(token queryToken, message string) error {
	position := token.pos
	if position < 0 || position > p.inputBytes {
		position = p.inputBytes
	}
	return queryError(position, message)
}

func queryError(position int, message string) error {
	return &QueryError{Position: position, Message: message}
}
