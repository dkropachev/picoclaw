package collectionquery

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var relativeTimestampPattern = regexp.MustCompile(`^-[1-9][0-9]*(m|h|d|w)$`)

// QueryError carries a safe bounded message and a zero-based UTF-8 byte
// position into the original query.
type QueryError struct {
	Position int    `json:"position"`
	Message  string `json:"message"`
}

func (err *QueryError) Error() string {
	if err == nil {
		return ErrInvalidQuery.Error()
	}
	return fmt.Sprintf("%v at byte %d: %s", ErrInvalidQuery, err.Position, err.Message)
}

func (err *QueryError) Unwrap() error { return ErrInvalidQuery }

// Parse parses bounded JQL-like input using only fields and operators declared
// by schema.
func Parse(input string, schema Schema) (Query, error) {
	normalizedSchema, err := NewSchema(schema.Fields, schema.DefaultOrder)
	if err != nil {
		return Query{}, err
	}
	if len(input) > MaxQueryBytes {
		return Query{}, newQueryError(MaxQueryBytes, fmt.Sprintf("query exceeds %d bytes", MaxQueryBytes))
	}
	if !utf8.ValidString(input) {
		return Query{}, newQueryError(firstInvalidUTF8Byte(input), "query is not valid UTF-8")
	}
	tokens, err := lex(input)
	if err != nil {
		return Query{}, err
	}
	parser := parser{schema: normalizedSchema, tokens: tokens, inputBytes: len(input)}
	query, err := parser.parse()
	if err != nil {
		return Query{}, err
	}
	if err := query.Validate(); err != nil {
		return Query{}, newQueryError(len(input), err.Error())
	}
	return query, nil
}

type tokenKind uint8

const (
	tokenEOF tokenKind = iota
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

type token struct {
	kind tokenKind
	text string
	pos  int
}

func lex(input string) ([]token, error) {
	tokens := make([]token, 0, len(input)/4+1)
	for offset := 0; offset < len(input); {
		switch input[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		case '(':
			tokens = append(tokens, token{kind: tokenLeftParen, text: "(", pos: offset})
			offset++
		case ')':
			tokens = append(tokens, token{kind: tokenRightParen, text: ")", pos: offset})
			offset++
		case ',':
			tokens = append(tokens, token{kind: tokenComma, text: ",", pos: offset})
			offset++
		case '=':
			tokens = append(tokens, token{kind: tokenEqual, text: "=", pos: offset})
			offset++
		case '~':
			tokens = append(tokens, token{kind: tokenContains, text: "~", pos: offset})
			offset++
		case '!':
			start := offset
			offset++
			if offset >= len(input) {
				return nil, newQueryError(start, "expected = or ~ after !")
			}
			switch input[offset] {
			case '=':
				tokens = append(tokens, token{kind: tokenNotEqual, text: "!=", pos: start})
			case '~':
				tokens = append(tokens, token{kind: tokenNotContains, text: "!~", pos: start})
			default:
				return nil, newQueryError(start, "expected = or ~ after !")
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
			tokens = append(tokens, token{kind: kind, text: input[start:offset], pos: start})
		case '\'', '"':
			value, next, err := lexQuoted(input, offset)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, value)
			offset = next
		default:
			start := offset
			for offset < len(input) && !delimiter(input[offset]) {
				offset++
			}
			if start == offset {
				return nil, newQueryError(start, "unexpected character")
			}
			tokens = append(tokens, token{kind: tokenWord, text: input[start:offset], pos: start})
		}
	}
	tokens = append(tokens, token{kind: tokenEOF, pos: len(input)})
	return tokens, nil
}

func delimiter(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', '(', ')', ',', '=', '!', '~', '>', '<', '\'', '"':
		return true
	default:
		return false
	}
}

func lexQuoted(input string, start int) (token, int, error) {
	quote := input[start]
	var value strings.Builder
	for offset := start + 1; offset < len(input); offset++ {
		character := input[offset]
		if character == quote {
			return token{kind: tokenString, text: value.String(), pos: start}, offset + 1, nil
		}
		if character != '\\' {
			value.WriteByte(character)
			continue
		}
		offset++
		if offset >= len(input) {
			return token{}, 0, newQueryError(start, "unterminated quoted string")
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
			return token{}, 0, newQueryError(offset-1, "unsupported string escape")
		}
	}
	return token{}, 0, newQueryError(start, "unterminated quoted string")
}

type parser struct {
	schema     Schema
	tokens     []token
	index      int
	inputBytes int
	depth      int
	predicates int
}

func (parser *parser) parse() (Query, error) {
	query := Query{schema: parser.schema.Clone()}
	var err error
	if parser.matchWord("ALL") {
		if !parser.atEnd() && !parser.startsOrderBy() {
			return Query{}, parser.errorAt(parser.peek(), "ALL must be the complete filter")
		}
	} else if !parser.atEnd() && !parser.startsOrderBy() {
		query.Filter, err = parser.parseOr()
		if err != nil {
			return Query{}, err
		}
	}
	if parser.startsOrderBy() {
		query.Order, err = parser.parseOrderBy()
		if err != nil {
			return Query{}, err
		}
	}
	if !parser.atEnd() {
		return Query{}, parser.errorAt(parser.peek(), "unexpected token "+strconv.Quote(parser.peek().text))
	}
	return query, nil
}

func (parser *parser) parseOr() (Expression, error) {
	left, err := parser.parseAnd()
	if err != nil {
		return nil, err
	}
	for parser.matchWord("OR") {
		right, parseErr := parser.parseAnd()
		if parseErr != nil {
			return nil, parseErr
		}
		left = LogicalExpression{Operator: LogicalOr, Left: left, Right: right}
	}
	return left, nil
}

func (parser *parser) parseAnd() (Expression, error) {
	left, err := parser.parseNot()
	if err != nil {
		return nil, err
	}
	for parser.matchWord("AND") {
		right, parseErr := parser.parseNot()
		if parseErr != nil {
			return nil, parseErr
		}
		left = LogicalExpression{Operator: LogicalAnd, Left: left, Right: right}
	}
	return left, nil
}

func (parser *parser) parseNot() (Expression, error) {
	if parser.matchWord("NOT") {
		if parser.depth >= MaxQueryDepth {
			return nil, parser.errorAt(parser.previous(), fmt.Sprintf("nesting exceeds %d", MaxQueryDepth))
		}
		parser.depth++
		expression, err := parser.parseNot()
		parser.depth--
		if err != nil {
			return nil, err
		}
		return Negation{Expression: expression}, nil
	}
	return parser.parsePrimary()
}

func (parser *parser) parsePrimary() (Expression, error) {
	if parser.match(tokenLeftParen) {
		open := parser.previous()
		if parser.depth >= MaxQueryDepth {
			return nil, parser.errorAt(open, fmt.Sprintf("nesting exceeds %d", MaxQueryDepth))
		}
		parser.depth++
		expression, err := parser.parseOr()
		parser.depth--
		if err != nil {
			return nil, err
		}
		if !parser.match(tokenRightParen) {
			return nil, parser.errorAt(parser.peek(), "expected )")
		}
		return expression, nil
	}
	return parser.parsePredicate()
}

func (parser *parser) parsePredicate() (Expression, error) {
	fieldToken := parser.peek()
	if !parser.match(tokenWord) {
		return nil, parser.errorAt(fieldToken, "expected query field")
	}
	fieldName := Field(strings.ToLower(fieldToken.text))
	field, ok := parser.schema.lookup(fieldName)
	if !ok {
		return nil, parser.errorAt(fieldToken, "unknown query field "+strconv.Quote(fieldToken.text))
	}
	parser.predicates++
	if parser.predicates > MaxQueryPredicates {
		return nil, parser.errorAt(fieldToken, fmt.Sprintf("query exceeds %d predicates", MaxQueryPredicates))
	}
	operator, err := parser.parseOperator()
	if err != nil {
		return nil, err
	}
	predicate := Predicate{Field: field.Name, Operator: operator}
	if operator == OperatorIn || operator == OperatorNotIn {
		if !parser.match(tokenLeftParen) {
			return nil, parser.errorAt(parser.peek(), "expected ( after "+string(operator))
		}
		for {
			if len(predicate.Values) >= MaxQueryINValues {
				return nil, parser.errorAt(parser.peek(), fmt.Sprintf("IN exceeds %d values", MaxQueryINValues))
			}
			value, valueErr := parser.parseValue(field)
			if valueErr != nil {
				return nil, valueErr
			}
			predicate.Values = append(predicate.Values, value)
			if !parser.match(tokenComma) {
				break
			}
		}
		if !parser.match(tokenRightParen) {
			return nil, parser.errorAt(parser.peek(), "expected ) after IN values")
		}
	} else {
		value, valueErr := parser.parseValue(field)
		if valueErr != nil {
			return nil, valueErr
		}
		predicate.Values = []Value{value}
	}
	if err := validatePredicate(parser.schema, predicate); err != nil {
		return nil, parser.errorAt(fieldToken, err.Error())
	}
	return predicate, nil
}

func (parser *parser) parseOperator() (Operator, error) {
	value := parser.peek()
	parser.index++
	var operator Operator
	switch value.kind {
	case tokenEqual:
		operator = OperatorEqual
	case tokenNotEqual:
		operator = OperatorNotEqual
	case tokenContains:
		operator = OperatorContains
	case tokenNotContains:
		operator = OperatorNotContains
	case tokenGreater:
		operator = OperatorGreater
	case tokenGreaterEq:
		operator = OperatorGreaterEq
	case tokenLess:
		operator = OperatorLess
	case tokenLessEq:
		operator = OperatorLessEq
	case tokenWord:
		if strings.EqualFold(value.text, "IN") {
			operator = OperatorIn
		} else if strings.EqualFold(value.text, "NOT") && parser.matchWord("IN") {
			operator = OperatorNotIn
		}
	}
	if operator != "" {
		return operator, nil
	}
	if value.kind == tokenEOF {
		parser.index--
	}
	return "", parser.errorAt(value, "expected query operator")
}

func (parser *parser) parseValue(field FieldSchema) (Value, error) {
	value := parser.peek()
	if value.kind != tokenWord && value.kind != tokenString {
		return Value{}, parser.errorAt(value, "expected query value")
	}
	parser.index++
	raw := value.text
	switch field.Type {
	case TypeBoolean:
		if strings.EqualFold(raw, "true") {
			return Value{Kind: ValueBoolean, Boolean: true}, nil
		}
		if strings.EqualFold(raw, "false") {
			return Value{Kind: ValueBoolean}, nil
		}
		return Value{}, parser.errorAt(value, "expected true or false")
	case TypeNumber:
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return Value{}, parser.errorAt(value, "expected a finite number")
		}
		if number == 0 {
			number = 0
		}
		return Value{Kind: ValueNumber, Number: number}, nil
	case TypeTimestamp:
		return parseTimestampValue(raw, value.pos)
	default:
		raw = strings.ToLower(raw)
		return Value{Kind: ValueString, Text: raw}, nil
	}
}

func parseTimestampValue(raw string, position int) (Value, error) {
	if relativeTimestampPattern.MatchString(strings.ToLower(raw)) {
		unit := raw[len(raw)-1]
		amount, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
		if err != nil {
			return Value{}, newQueryError(position, "relative date is too large")
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
			return Value{}, newQueryError(position, "relative date is too large")
		}
		offset := -time.Duration(amount) * multiplier
		return Value{Kind: ValueRelativeTimestamp, Text: strings.ToLower(raw), TimeOffset: offset}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		if date, dateErr := time.Parse("2006-01-02", raw); dateErr == nil {
			parsed = date
		} else {
			return Value{}, newQueryError(position, "expected ISO timestamp or relative date")
		}
	}
	return Value{Kind: ValueTimestamp, Timestamp: parsed.UTC()}, nil
}

func (parser *parser) parseOrderBy() ([]SortField, error) {
	parser.index += 2
	order := make([]SortField, 0, MaxQuerySortFields)
	seen := make(map[Field]struct{}, MaxQuerySortFields)
	for {
		if len(order) >= MaxQuerySortFields {
			return nil, parser.errorAt(parser.peek(), fmt.Sprintf("ORDER BY exceeds %d fields", MaxQuerySortFields))
		}
		fieldToken := parser.peek()
		if !parser.match(tokenWord) {
			return nil, parser.errorAt(fieldToken, "expected ORDER BY field")
		}
		fieldName := Field(strings.ToLower(fieldToken.text))
		field, ok := parser.schema.lookup(fieldName)
		if !ok || !field.Sortable {
			return nil, parser.errorAt(fieldToken, "field cannot be sorted")
		}
		if _, duplicate := seen[field.Name]; duplicate {
			return nil, parser.errorAt(fieldToken, "duplicate ORDER BY field")
		}
		seen[field.Name] = struct{}{}
		directionToken := parser.peek()
		if !parser.match(tokenWord) {
			return nil, parser.errorAt(directionToken, "expected ASC or DESC")
		}
		direction := Direction(strings.ToUpper(directionToken.text))
		if direction != Ascending && direction != Descending {
			return nil, parser.errorAt(directionToken, "expected ASC or DESC")
		}
		order = append(order, SortField{Field: field.Name, Direction: direction})
		if !parser.match(tokenComma) {
			break
		}
	}
	return order, nil
}

func (parser *parser) startsOrderBy() bool {
	return parser.index+1 < len(parser.tokens) && parser.tokens[parser.index].kind == tokenWord &&
		strings.EqualFold(parser.tokens[parser.index].text, "ORDER") &&
		parser.tokens[parser.index+1].kind == tokenWord &&
		strings.EqualFold(parser.tokens[parser.index+1].text, "BY")
}

func (parser *parser) atEnd() bool     { return parser.peek().kind == tokenEOF }
func (parser *parser) peek() token     { return parser.tokens[parser.index] }
func (parser *parser) previous() token { return parser.tokens[parser.index-1] }

func (parser *parser) match(kind tokenKind) bool {
	if parser.peek().kind != kind {
		return false
	}
	parser.index++
	return true
}

func (parser *parser) matchWord(word string) bool {
	if parser.peek().kind != tokenWord || !strings.EqualFold(parser.peek().text, word) {
		return false
	}
	parser.index++
	return true
}

func (parser *parser) errorAt(value token, message string) error {
	position := value.pos
	if position < 0 || position > parser.inputBytes {
		position = parser.inputBytes
	}
	return newQueryError(position, message)
}

func newQueryError(position int, message string) *QueryError {
	if position < 0 {
		position = 0
	}
	message = safeQueryErrorMessage(message)
	return &QueryError{Position: position, Message: message}
}

func safeQueryErrorMessage(message string) string {
	message = strings.TrimSpace(strings.ToValidUTF8(message, ""))
	message = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		message = "invalid query"
	}
	if len(message) <= MaxQueryErrorMessageLen {
		return message
	}
	message = message[:MaxQueryErrorMessageLen]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return strings.TrimSpace(message)
}

func firstInvalidUTF8Byte(value string) int {
	for offset := 0; offset < len(value); {
		_, size := utf8.DecodeRuneInString(value[offset:])
		if size == 1 && value[offset] >= utf8.RuneSelf {
			return offset
		}
		offset += size
	}
	return 0
}
