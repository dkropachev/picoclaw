package repoaudit

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	maxRepositoryReviewGuardExpressionBytes  = 4096
	maxRepositoryReviewGuardExpressionTokens = 256
	maxRepositoryReviewGuardExpressionDepth  = 16
)

var (
	// ErrInvalidRepositoryReviewGuardExpression identifies a malformed or
	// unsupported repository-review admission expression.
	ErrInvalidRepositoryReviewGuardExpression = errors.New("invalid repository review guard expression")
	// ErrRepositoryReviewGuardUnknown identifies an expression whose result
	// depends on accounting or account-limit data that is not currently known.
	ErrRepositoryReviewGuardUnknown = errors.New("repository review guard expression result is unknown")
)

// RepositoryReviewGuardUnknownError reports the fields which prevented an
// admission expression from producing a definite result. Callers must treat
// this error as a denial; EvaluateRepositoryReviewGuardExpression returns
// false with it.
type RepositoryReviewGuardUnknownError struct {
	Fields []string
}

func (err *RepositoryReviewGuardUnknownError) Error() string {
	if err == nil || len(err.Fields) == 0 {
		return ErrRepositoryReviewGuardUnknown.Error()
	}
	return fmt.Sprintf("%s: %s", ErrRepositoryReviewGuardUnknown, strings.Join(err.Fields, ", "))
}

func (err *RepositoryReviewGuardUnknownError) Unwrap() error {
	return ErrRepositoryReviewGuardUnknown
}

// RepositoryReviewGuardEnvironment is the bounded accounting snapshot used
// when a worker is about to admit its next repository-review task.
//
// AccountLimitSnapshots must contain only snapshots for the account selected
// by the profile. AccountLimitsKnown says that limit discovery completed; a
// false value keeps absence and partial data unknown. Multiple snapshots for
// the same normalized window are combined conservatively by using the lowest
// remaining percentage (and therefore the highest used percentage).
type RepositoryReviewGuardEnvironment struct {
	SpentTokens           RepositoryReviewTokenUsage
	SpendTotalUSD         float64
	CostKnown             bool
	AccountLimitsKnown    bool
	AccountLimitSnapshots []RepositoryReviewAccountLimitSnapshot
}

// ValidateRepositoryReviewGuardExpression parses and type-checks expression
// without evaluating runtime values. An empty expression is valid and means
// "allow".
func ValidateRepositoryReviewGuardExpression(expression string) error {
	_, err := parseRepositoryReviewGuardExpression(expression)
	return err
}

// EvaluateRepositoryReviewGuardExpression evaluates expression against a
// point-in-time worker accounting snapshot. An empty expression permits work.
// A final unknown result is always returned as false plus a typed error.
func EvaluateRepositoryReviewGuardExpression(
	expression string,
	environment RepositoryReviewGuardEnvironment,
) (bool, error) {
	node, err := parseRepositoryReviewGuardExpression(expression)
	if err != nil {
		return false, err
	}
	if node == nil {
		return true, nil
	}

	resolver, err := newRepositoryReviewGuardResolver(environment)
	if err != nil {
		return false, err
	}
	result := node.evaluate(resolver)
	if !result.known {
		fields := sortedGuardUnknownFields(result.unknownFields)
		return false, &RepositoryReviewGuardUnknownError{Fields: fields}
	}
	if result.kind != guardValueBoolean {
		return false, invalidRepositoryReviewGuardExpressionf("expression does not produce a boolean result")
	}
	return result.boolean, nil
}

// RepositoryReviewGuardUsesAccountLimits reports whether a valid expression
// reads account.limits. Invalid expressions return false; callers should
// validate persisted input separately with
// ValidateRepositoryReviewGuardExpression.
func RepositoryReviewGuardUsesAccountLimits(expression string) bool {
	return repositoryReviewGuardExpressionUsesPrefix(expression, "account.limits")
}

// RepositoryReviewGuardUsesSpend reports whether a valid expression reads
// monetary spend. Token counters under spent.tokens do not require model-price
// metadata and are not included.
func RepositoryReviewGuardUsesSpend(expression string) bool {
	return repositoryReviewGuardExpressionUsesPrefix(expression, "spend.total")
}

type repositoryReviewGuardTokenKind uint8

const (
	guardTokenEOF repositoryReviewGuardTokenKind = iota
	guardTokenIdentifier
	guardTokenNumber
	guardTokenString
	guardTokenBoolean
	guardTokenAnd
	guardTokenOr
	guardTokenNot
	guardTokenLeftParen
	guardTokenRightParen
	guardTokenEqual
	guardTokenNotEqual
	guardTokenLess
	guardTokenLessEqual
	guardTokenGreater
	guardTokenGreaterEqual
)

type repositoryReviewGuardToken struct {
	kind     repositoryReviewGuardTokenKind
	text     string
	position int
}

func tokenizeRepositoryReviewGuardExpression(expression string) ([]repositoryReviewGuardToken, error) {
	if len(expression) > maxRepositoryReviewGuardExpressionBytes {
		return nil, invalidRepositoryReviewGuardExpressionf(
			"expression exceeds %d bytes", maxRepositoryReviewGuardExpressionBytes,
		)
	}

	tokens := make([]repositoryReviewGuardToken, 0, 32)
	appendToken := func(kind repositoryReviewGuardTokenKind, text string, position int) error {
		if len(tokens) >= maxRepositoryReviewGuardExpressionTokens {
			return invalidRepositoryReviewGuardExpressionf(
				"expression exceeds %d tokens", maxRepositoryReviewGuardExpressionTokens,
			)
		}
		tokens = append(tokens, repositoryReviewGuardToken{kind: kind, text: text, position: position})
		return nil
	}

	for position := 0; position < len(expression); {
		character := expression[position]
		if isRepositoryReviewGuardWhitespace(character) {
			position++
			continue
		}

		start := position
		switch character {
		case '(':
			position++
			if err := appendToken(guardTokenLeftParen, "(", start); err != nil {
				return nil, err
			}
		case ')':
			position++
			if err := appendToken(guardTokenRightParen, ")", start); err != nil {
				return nil, err
			}
		case '*':
			return nil, invalidRepositoryReviewGuardExpressionf(
				"wildcards are not supported at byte %d; '*' names a field family in documentation, not expression syntax",
				start+1,
			)
		case '=':
			position++
			text := "="
			if position < len(expression) && expression[position] == '=' {
				position++
				text = "=="
			}
			if err := appendToken(guardTokenEqual, text, start); err != nil {
				return nil, err
			}
		case '!':
			position++
			if position >= len(expression) || expression[position] != '=' {
				return nil, invalidRepositoryReviewGuardExpressionf("expected '=' after '!' at byte %d", start+1)
			}
			position++
			if err := appendToken(guardTokenNotEqual, "!=", start); err != nil {
				return nil, err
			}
		case '<':
			position++
			kind, text := guardTokenLess, "<"
			if position < len(expression) && expression[position] == '=' {
				position++
				kind, text = guardTokenLessEqual, "<="
			}
			if err := appendToken(kind, text, start); err != nil {
				return nil, err
			}
		case '>':
			position++
			kind, text := guardTokenGreater, ">"
			if position < len(expression) && expression[position] == '=' {
				position++
				kind, text = guardTokenGreaterEqual, ">="
			}
			if err := appendToken(kind, text, start); err != nil {
				return nil, err
			}
		case '\'', '"':
			text, next, err := scanRepositoryReviewGuardString(expression, position)
			if err != nil {
				return nil, err
			}
			position = next
			if err := appendToken(guardTokenString, text, start); err != nil {
				return nil, err
			}
		default:
			if isRepositoryReviewGuardNumberStart(expression, position) {
				text, next, err := scanRepositoryReviewGuardNumber(expression, position)
				if err != nil {
					return nil, err
				}
				position = next
				if err := appendToken(guardTokenNumber, text, start); err != nil {
					return nil, err
				}
				continue
			}
			if isRepositoryReviewGuardIdentifierStart(character) {
				position++
				for position < len(expression) && isRepositoryReviewGuardIdentifierPart(expression[position]) {
					position++
				}
				text := expression[start:position]
				if strings.HasSuffix(text, ".") && position < len(expression) && expression[position] == '*' {
					return nil, invalidRepositoryReviewGuardExpressionf(
						"wildcards are not supported at byte %d; '*' names a field family in documentation, not expression syntax",
						position+1,
					)
				}
				lower := strings.ToLower(text)
				kind := guardTokenIdentifier
				switch lower {
				case "and":
					kind = guardTokenAnd
				case "or":
					kind = guardTokenOr
				case "not":
					kind = guardTokenNot
				case "true", "false":
					kind = guardTokenBoolean
				}
				if kind == guardTokenIdentifier {
					if err := validateRepositoryReviewGuardIdentifierShape(lower); err != nil {
						return nil, invalidRepositoryReviewGuardExpressionf("identifier at byte %d: %v", start+1, err)
					}
					text = lower
				} else {
					text = lower
				}
				if err := appendToken(kind, text, start); err != nil {
					return nil, err
				}
				continue
			}
			return nil, invalidRepositoryReviewGuardExpressionf(
				"unexpected character %q at byte %d", character, start+1,
			)
		}
	}

	tokens = append(tokens, repositoryReviewGuardToken{kind: guardTokenEOF, position: len(expression)})
	return tokens, nil
}

func scanRepositoryReviewGuardString(expression string, start int) (string, int, error) {
	quote := expression[start]
	var value strings.Builder
	value.Grow(16)
	for position := start + 1; position < len(expression); position++ {
		character := expression[position]
		if character == quote {
			return value.String(), position + 1, nil
		}
		if character == '\n' || character == '\r' {
			return "", 0, invalidRepositoryReviewGuardExpressionf(
				"unescaped newline in string starting at byte %d", start+1,
			)
		}
		if character != '\\' {
			value.WriteByte(character)
			continue
		}
		position++
		if position >= len(expression) {
			return "", 0, invalidRepositoryReviewGuardExpressionf(
				"unterminated escape in string starting at byte %d", start+1,
			)
		}
		switch escaped := expression[position]; escaped {
		case '\\', '\'', '"':
			value.WriteByte(escaped)
		case 'n':
			value.WriteByte('\n')
		case 'r':
			value.WriteByte('\r')
		case 't':
			value.WriteByte('\t')
		case 'b':
			value.WriteByte('\b')
		case 'f':
			value.WriteByte('\f')
		default:
			return "", 0, invalidRepositoryReviewGuardExpressionf(
				"unsupported escape \\%c at byte %d", escaped, position,
			)
		}
	}
	return "", 0, invalidRepositoryReviewGuardExpressionf(
		"unterminated string starting at byte %d", start+1,
	)
}

func scanRepositoryReviewGuardNumber(expression string, start int) (string, int, error) {
	position := start
	if expression[position] == '+' || expression[position] == '-' {
		position++
	}
	digitsBefore := 0
	for position < len(expression) && isRepositoryReviewGuardDigit(expression[position]) {
		position++
		digitsBefore++
	}
	digitsAfter := 0
	if position < len(expression) && expression[position] == '.' {
		position++
		for position < len(expression) && isRepositoryReviewGuardDigit(expression[position]) {
			position++
			digitsAfter++
		}
	}
	if digitsBefore == 0 && digitsAfter == 0 {
		return "", 0, invalidRepositoryReviewGuardExpressionf("invalid number at byte %d", start+1)
	}
	if position < len(expression) && (expression[position] == 'e' || expression[position] == 'E') {
		position++
		if position < len(expression) && (expression[position] == '+' || expression[position] == '-') {
			position++
		}
		exponentDigits := 0
		for position < len(expression) && isRepositoryReviewGuardDigit(expression[position]) {
			position++
			exponentDigits++
		}
		if exponentDigits == 0 {
			return "", 0, invalidRepositoryReviewGuardExpressionf("invalid exponent at byte %d", start+1)
		}
	}
	text := expression[start:position]
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", 0, invalidRepositoryReviewGuardExpressionf("invalid finite number %q at byte %d", text, start+1)
	}
	return text, position, nil
}

func isRepositoryReviewGuardNumberStart(expression string, position int) bool {
	character := expression[position]
	if isRepositoryReviewGuardDigit(character) || character == '.' {
		return true
	}
	return (character == '+' || character == '-') && position+1 < len(expression) &&
		(isRepositoryReviewGuardDigit(expression[position+1]) || expression[position+1] == '.')
}

func isRepositoryReviewGuardWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == '\r'
}

func isRepositoryReviewGuardDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func isRepositoryReviewGuardIdentifierStart(character byte) bool {
	return character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func isRepositoryReviewGuardIdentifierPart(character byte) bool {
	return isRepositoryReviewGuardIdentifierStart(character) || isRepositoryReviewGuardDigit(character) ||
		character == '.' || character == '-'
}

func validateRepositoryReviewGuardIdentifierShape(identifier string) error {
	if strings.HasPrefix(identifier, ".") || strings.HasSuffix(identifier, ".") || strings.Contains(identifier, "..") {
		return errors.New("dotted identifiers require a non-empty segment on each side of '.'")
	}
	return nil
}

type repositoryReviewGuardParser struct {
	tokens []repositoryReviewGuardToken
	index  int
}

func parseRepositoryReviewGuardExpression(expression string) (repositoryReviewGuardNode, error) {
	if strings.TrimSpace(expression) == "" {
		if len(expression) > maxRepositoryReviewGuardExpressionBytes {
			return nil, invalidRepositoryReviewGuardExpressionf(
				"expression exceeds %d bytes", maxRepositoryReviewGuardExpressionBytes,
			)
		}
		return nil, nil
	}
	tokens, err := tokenizeRepositoryReviewGuardExpression(expression)
	if err != nil {
		return nil, err
	}
	parser := &repositoryReviewGuardParser{tokens: tokens}
	node, err := parser.parseOr(0)
	if err != nil {
		return nil, err
	}
	if token := parser.peek(); token.kind != guardTokenEOF {
		return nil, invalidRepositoryReviewGuardExpressionf(
			"unexpected token %q at byte %d", token.text, token.position+1,
		)
	}
	return node, nil
}

func (parser *repositoryReviewGuardParser) parseOr(depth int) (repositoryReviewGuardNode, error) {
	left, err := parser.parseAnd(depth)
	if err != nil {
		return nil, err
	}
	for parser.match(guardTokenOr) {
		right, err := parser.parseAnd(depth)
		if err != nil {
			return nil, err
		}
		left = &repositoryReviewGuardLogicalNode{operator: guardTokenOr, left: left, right: right}
	}
	return left, nil
}

func (parser *repositoryReviewGuardParser) parseAnd(depth int) (repositoryReviewGuardNode, error) {
	left, err := parser.parseUnary(depth)
	if err != nil {
		return nil, err
	}
	for parser.match(guardTokenAnd) {
		right, err := parser.parseUnary(depth)
		if err != nil {
			return nil, err
		}
		left = &repositoryReviewGuardLogicalNode{operator: guardTokenAnd, left: left, right: right}
	}
	return left, nil
}

func (parser *repositoryReviewGuardParser) parseUnary(depth int) (repositoryReviewGuardNode, error) {
	if parser.match(guardTokenNot) {
		if depth >= maxRepositoryReviewGuardExpressionDepth {
			return nil, invalidRepositoryReviewGuardExpressionf(
				"expression nesting exceeds %d", maxRepositoryReviewGuardExpressionDepth,
			)
		}
		child, err := parser.parseUnary(depth + 1)
		if err != nil {
			return nil, err
		}
		return &repositoryReviewGuardNotNode{child: child}, nil
	}
	return parser.parsePrimary(depth)
}

func (parser *repositoryReviewGuardParser) parsePrimary(depth int) (repositoryReviewGuardNode, error) {
	if parser.match(guardTokenLeftParen) {
		if depth >= maxRepositoryReviewGuardExpressionDepth {
			return nil, invalidRepositoryReviewGuardExpressionf(
				"expression nesting exceeds %d", maxRepositoryReviewGuardExpressionDepth,
			)
		}
		node, err := parser.parseOr(depth + 1)
		if err != nil {
			return nil, err
		}
		if !parser.match(guardTokenRightParen) {
			token := parser.peek()
			return nil, invalidRepositoryReviewGuardExpressionf("expected ')' at byte %d", token.position+1)
		}
		return node, nil
	}

	left, err := parser.parseOperand()
	if err != nil {
		return nil, err
	}
	operator := parser.peek()
	if !isRepositoryReviewGuardComparison(operator.kind) {
		if left.valueKind() != guardValueBoolean {
			return nil, invalidRepositoryReviewGuardExpressionf(
				"%q at byte %d must be compared to produce a boolean result", operatorText(left), operator.position+1,
			)
		}
		return left, nil
	}
	parser.index++
	right, err := parser.parseOperand()
	if err != nil {
		return nil, err
	}
	if err := validateRepositoryReviewGuardComparison(operator, left.valueKind(), right.valueKind()); err != nil {
		return nil, err
	}
	return &repositoryReviewGuardComparisonNode{operator: operator.kind, left: left, right: right}, nil
}

func (parser *repositoryReviewGuardParser) parseOperand() (repositoryReviewGuardNode, error) {
	token := parser.peek()
	switch token.kind {
	case guardTokenIdentifier:
		kind, err := repositoryReviewGuardIdentifierKind(token.text)
		if err != nil {
			return nil, invalidRepositoryReviewGuardExpressionf("%v at byte %d", err, token.position+1)
		}
		parser.index++
		return &repositoryReviewGuardIdentifierNode{identifier: token.text, kind: kind}, nil
	case guardTokenNumber:
		value, err := strconv.ParseFloat(token.text, 64)
		if err != nil {
			return nil, invalidRepositoryReviewGuardExpressionf("invalid number at byte %d", token.position+1)
		}
		parser.index++
		return &repositoryReviewGuardLiteralNode{value: knownGuardNumber(value), text: token.text}, nil
	case guardTokenString:
		parser.index++
		return &repositoryReviewGuardLiteralNode{value: knownGuardString(token.text), text: token.text}, nil
	case guardTokenBoolean:
		parser.index++
		return &repositoryReviewGuardLiteralNode{value: knownGuardBoolean(token.text == "true"), text: token.text}, nil
	default:
		return nil, invalidRepositoryReviewGuardExpressionf(
			"expected an identifier or literal at byte %d", token.position+1,
		)
	}
}

func (parser *repositoryReviewGuardParser) peek() repositoryReviewGuardToken {
	return parser.tokens[parser.index]
}

func (parser *repositoryReviewGuardParser) match(kind repositoryReviewGuardTokenKind) bool {
	if parser.peek().kind != kind {
		return false
	}
	parser.index++
	return true
}

func isRepositoryReviewGuardComparison(kind repositoryReviewGuardTokenKind) bool {
	switch kind {
	case guardTokenEqual, guardTokenNotEqual, guardTokenLess, guardTokenLessEqual,
		guardTokenGreater, guardTokenGreaterEqual:
		return true
	default:
		return false
	}
}

func validateRepositoryReviewGuardComparison(
	operator repositoryReviewGuardToken,
	leftKind, rightKind repositoryReviewGuardValueKind,
) error {
	if leftKind != rightKind {
		return invalidRepositoryReviewGuardExpressionf(
			"comparison %q at byte %d has mismatched operand types", operator.text, operator.position+1,
		)
	}
	if operator.kind != guardTokenEqual && operator.kind != guardTokenNotEqual && leftKind == guardValueBoolean {
		return invalidRepositoryReviewGuardExpressionf(
			"boolean values support only '='/'==' and '!=' at byte %d", operator.position+1,
		)
	}
	return nil
}

func repositoryReviewGuardIdentifierKind(identifier string) (repositoryReviewGuardValueKind, error) {
	switch identifier {
	case "spent.tokens.prompt", "spent.tokens.completion", "spent.tokens.cached", "spent.tokens.total",
		"spend.total.usd":
		return guardValueNumber, nil
	case "account.limits.known", "account.limits.exhausted_known", "account.limits.exhausted", "account.limits.any":
		return guardValueBoolean, nil
	}

	parts := strings.Split(identifier, ".")
	if len(parts) == 4 && parts[0] == "account" && parts[1] == "limits" && parts[2] != "" {
		if normalized := normalizeRepositoryReviewGuardLimitWindow(
			parts[2],
		); normalized == "" ||
			normalized != parts[2] {
			return guardValueInvalid, fmt.Errorf("account-limit window %q is not normalized", parts[2])
		}
		switch parts[3] {
		case "known", "observed":
			return guardValueBoolean, nil
		case "remaining_percent", "used_percent", "minimum_remaining_percent", "maximum_used_percent":
			return guardValueNumber, nil
		}
	}
	return guardValueInvalid, fmt.Errorf("unsupported guard field %q", identifier)
}

type repositoryReviewGuardValueKind uint8

const (
	guardValueInvalid repositoryReviewGuardValueKind = iota
	guardValueBoolean
	guardValueNumber
	guardValueString
)

type repositoryReviewGuardValue struct {
	kind          repositoryReviewGuardValueKind
	known         bool
	boolean       bool
	number        float64
	text          string
	unknownFields map[string]struct{}
}

func knownGuardBoolean(value bool) repositoryReviewGuardValue {
	return repositoryReviewGuardValue{kind: guardValueBoolean, known: true, boolean: value}
}

func knownGuardNumber(value float64) repositoryReviewGuardValue {
	return repositoryReviewGuardValue{kind: guardValueNumber, known: true, number: value}
}

func knownGuardString(value string) repositoryReviewGuardValue {
	return repositoryReviewGuardValue{kind: guardValueString, known: true, text: value}
}

func unknownGuardValue(kind repositoryReviewGuardValueKind, field string) repositoryReviewGuardValue {
	return repositoryReviewGuardValue{
		kind: kind, unknownFields: map[string]struct{}{field: {}},
	}
}

type repositoryReviewGuardNode interface {
	valueKind() repositoryReviewGuardValueKind
	evaluate(resolver *repositoryReviewGuardResolver) repositoryReviewGuardValue
	collectIdentifiers(identifiers map[string]struct{})
}

type repositoryReviewGuardLiteralNode struct {
	value repositoryReviewGuardValue
	text  string
}

func (node *repositoryReviewGuardLiteralNode) valueKind() repositoryReviewGuardValueKind {
	return node.value.kind
}

func (node *repositoryReviewGuardLiteralNode) evaluate(*repositoryReviewGuardResolver) repositoryReviewGuardValue {
	return node.value
}

func (*repositoryReviewGuardLiteralNode) collectIdentifiers(map[string]struct{}) {}

type repositoryReviewGuardIdentifierNode struct {
	identifier string
	kind       repositoryReviewGuardValueKind
}

func (node *repositoryReviewGuardIdentifierNode) valueKind() repositoryReviewGuardValueKind {
	return node.kind
}

func (node *repositoryReviewGuardIdentifierNode) evaluate(
	resolver *repositoryReviewGuardResolver,
) repositoryReviewGuardValue {
	return resolver.resolve(node.identifier, node.kind)
}

func (node *repositoryReviewGuardIdentifierNode) collectIdentifiers(identifiers map[string]struct{}) {
	identifiers[node.identifier] = struct{}{}
}

type repositoryReviewGuardComparisonNode struct {
	operator    repositoryReviewGuardTokenKind
	left, right repositoryReviewGuardNode
}

func (*repositoryReviewGuardComparisonNode) valueKind() repositoryReviewGuardValueKind {
	return guardValueBoolean
}

func (node *repositoryReviewGuardComparisonNode) evaluate(
	resolver *repositoryReviewGuardResolver,
) repositoryReviewGuardValue {
	left := node.left.evaluate(resolver)
	right := node.right.evaluate(resolver)
	if !left.known || !right.known {
		return repositoryReviewGuardValue{
			kind: guardValueBoolean, unknownFields: mergeGuardUnknownFields(left, right),
		}
	}

	comparison := 0
	switch left.kind {
	case guardValueBoolean:
		if left.boolean != right.boolean {
			if !left.boolean {
				comparison = -1
			} else {
				comparison = 1
			}
		}
	case guardValueNumber:
		if left.number < right.number {
			comparison = -1
		} else if left.number > right.number {
			comparison = 1
		}
	case guardValueString:
		comparison = strings.Compare(left.text, right.text)
	}

	var result bool
	switch node.operator {
	case guardTokenEqual:
		result = comparison == 0
	case guardTokenNotEqual:
		result = comparison != 0
	case guardTokenLess:
		result = comparison < 0
	case guardTokenLessEqual:
		result = comparison <= 0
	case guardTokenGreater:
		result = comparison > 0
	case guardTokenGreaterEqual:
		result = comparison >= 0
	}
	return knownGuardBoolean(result)
}

func (node *repositoryReviewGuardComparisonNode) collectIdentifiers(identifiers map[string]struct{}) {
	node.left.collectIdentifiers(identifiers)
	node.right.collectIdentifiers(identifiers)
}

type repositoryReviewGuardLogicalNode struct {
	operator    repositoryReviewGuardTokenKind
	left, right repositoryReviewGuardNode
}

func (*repositoryReviewGuardLogicalNode) valueKind() repositoryReviewGuardValueKind {
	return guardValueBoolean
}

func (node *repositoryReviewGuardLogicalNode) evaluate(
	resolver *repositoryReviewGuardResolver,
) repositoryReviewGuardValue {
	left := node.left.evaluate(resolver)
	right := node.right.evaluate(resolver)
	if node.operator == guardTokenAnd {
		if left.known && !left.boolean || right.known && !right.boolean {
			return knownGuardBoolean(false)
		}
		if left.known && right.known {
			return knownGuardBoolean(true)
		}
	} else {
		if left.known && left.boolean || right.known && right.boolean {
			return knownGuardBoolean(true)
		}
		if left.known && right.known {
			return knownGuardBoolean(false)
		}
	}
	return repositoryReviewGuardValue{
		kind: guardValueBoolean, unknownFields: mergeGuardUnknownFields(left, right),
	}
}

func (node *repositoryReviewGuardLogicalNode) collectIdentifiers(identifiers map[string]struct{}) {
	node.left.collectIdentifiers(identifiers)
	node.right.collectIdentifiers(identifiers)
}

type repositoryReviewGuardNotNode struct {
	child repositoryReviewGuardNode
}

func (*repositoryReviewGuardNotNode) valueKind() repositoryReviewGuardValueKind {
	return guardValueBoolean
}

func (node *repositoryReviewGuardNotNode) evaluate(
	resolver *repositoryReviewGuardResolver,
) repositoryReviewGuardValue {
	value := node.child.evaluate(resolver)
	if !value.known {
		return repositoryReviewGuardValue{kind: guardValueBoolean, unknownFields: value.unknownFields}
	}
	return knownGuardBoolean(!value.boolean)
}

func (node *repositoryReviewGuardNotNode) collectIdentifiers(identifiers map[string]struct{}) {
	node.child.collectIdentifiers(identifiers)
}

func mergeGuardUnknownFields(values ...repositoryReviewGuardValue) map[string]struct{} {
	var merged map[string]struct{}
	for _, value := range values {
		for field := range value.unknownFields {
			if merged == nil {
				merged = make(map[string]struct{})
			}
			merged[field] = struct{}{}
		}
	}
	return merged
}

func sortedGuardUnknownFields(fields map[string]struct{}) []string {
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

type repositoryReviewGuardLimitAggregate struct {
	exists           bool
	known            bool
	observed         bool
	remainingPercent float64
	exhausted        repositoryReviewGuardValue
}

type repositoryReviewGuardLimitAccumulator struct {
	exists       bool
	allKnown     bool
	observed     bool
	remaining    float64
	knownExhaust bool
}

type repositoryReviewGuardResolver struct {
	values       map[string]repositoryReviewGuardValue
	limitOverall repositoryReviewGuardLimitAggregate
	limitWindows map[string]repositoryReviewGuardLimitAggregate
}

func newRepositoryReviewGuardResolver(
	environment RepositoryReviewGuardEnvironment,
) (*repositoryReviewGuardResolver, error) {
	tokens := environment.SpentTokens
	if tokens.PromptTokens < 0 || tokens.CompletionTokens < 0 || tokens.CachedTokens < 0 || tokens.TotalTokens < 0 {
		return nil, invalidRepositoryReviewGuardEnvironmentf("token counters must be non-negative")
	}
	if tokens.CompletionTokens > (1<<63-1)-tokens.PromptTokens {
		return nil, invalidRepositoryReviewGuardEnvironmentf("token counters overflow their total")
	}
	minimumTotalTokens := tokens.PromptTokens + tokens.CompletionTokens
	totalTokens := tokens.TotalTokens
	if totalTokens == 0 && minimumTotalTokens > 0 {
		totalTokens = minimumTotalTokens
	}
	if totalTokens < minimumTotalTokens || tokens.CachedTokens > tokens.PromptTokens {
		return nil, invalidRepositoryReviewGuardEnvironmentf("token counters are inconsistent")
	}
	if environment.CostKnown && (environment.SpendTotalUSD < 0 || math.IsNaN(environment.SpendTotalUSD) ||
		math.IsInf(environment.SpendTotalUSD, 0)) {
		return nil, invalidRepositoryReviewGuardEnvironmentf("known monetary spend must be finite and non-negative")
	}

	resolver := &repositoryReviewGuardResolver{
		values: map[string]repositoryReviewGuardValue{
			"spent.tokens.prompt":     knownGuardNumber(float64(tokens.PromptTokens)),
			"spent.tokens.completion": knownGuardNumber(float64(tokens.CompletionTokens)),
			"spent.tokens.cached":     knownGuardNumber(float64(tokens.CachedTokens)),
			"spent.tokens.total":      knownGuardNumber(float64(totalTokens)),
		},
	}
	if environment.CostKnown {
		resolver.values["spend.total.usd"] = knownGuardNumber(environment.SpendTotalUSD)
	} else {
		resolver.values["spend.total.usd"] = unknownGuardValue(guardValueNumber, "spend.total.usd")
	}
	if err := resolver.aggregateAccountLimits(environment); err != nil {
		return nil, err
	}
	return resolver, nil
}

func (resolver *repositoryReviewGuardResolver) aggregateAccountLimits(
	environment RepositoryReviewGuardEnvironment,
) error {
	newAccumulator := func() *repositoryReviewGuardLimitAccumulator {
		return &repositoryReviewGuardLimitAccumulator{allKnown: true, remaining: 100}
	}
	overall := newAccumulator()
	windows := make(map[string]*repositoryReviewGuardLimitAccumulator)

	for _, snapshot := range environment.AccountLimitSnapshots {
		window := normalizeRepositoryReviewGuardLimitWindow(snapshot.Window)
		if window == "" {
			return invalidRepositoryReviewGuardEnvironmentf("account-limit snapshot has an empty window")
		}
		windowAccumulator := windows[window]
		if windowAccumulator == nil {
			windowAccumulator = newAccumulator()
			windows[window] = windowAccumulator
		}
		for _, target := range []*repositoryReviewGuardLimitAccumulator{overall, windowAccumulator} {
			target.exists = true
			if snapshot.RemainingPercent == nil {
				target.allKnown = false
				continue
			}
			remaining := *snapshot.RemainingPercent
			target.observed = true
			if remaining < 0 || remaining > 100 || math.IsNaN(remaining) || math.IsInf(remaining, 0) {
				return invalidRepositoryReviewGuardEnvironmentf(
					"account-limit snapshot for %q has an invalid remaining percentage", snapshot.Window,
				)
			}
			if remaining < target.remaining {
				target.remaining = remaining
			}
			if remaining <= 0 {
				target.knownExhaust = true
			}
		}
	}

	resolver.limitOverall = finishRepositoryReviewGuardLimitAggregate(overall, environment.AccountLimitsKnown)
	resolver.limitWindows = make(map[string]repositoryReviewGuardLimitAggregate, len(windows))
	for window, accumulated := range windows {
		resolver.limitWindows[window] = finishRepositoryReviewGuardLimitAggregate(
			accumulated, environment.AccountLimitsKnown,
		)
	}
	return nil
}

func finishRepositoryReviewGuardLimitAggregate(
	accumulated *repositoryReviewGuardLimitAccumulator,
	complete bool,
) repositoryReviewGuardLimitAggregate {
	aggregate := repositoryReviewGuardLimitAggregate{
		exists:   accumulated.exists,
		known:    complete && accumulated.exists && accumulated.allKnown,
		observed: accumulated.observed,
	}
	if aggregate.observed {
		aggregate.remainingPercent = accumulated.remaining
	}
	switch {
	case accumulated.knownExhaust:
		aggregate.exhausted = knownGuardBoolean(true)
	case complete && accumulated.allKnown:
		aggregate.exhausted = knownGuardBoolean(false)
	default:
		aggregate.exhausted = unknownGuardValue(guardValueBoolean, "account.limits.exhausted")
	}
	return aggregate
}

func (resolver *repositoryReviewGuardResolver) resolve(
	identifier string,
	kind repositoryReviewGuardValueKind,
) repositoryReviewGuardValue {
	if value, exists := resolver.values[identifier]; exists {
		return value
	}
	switch identifier {
	case "account.limits.known":
		known := resolver.limitOverall.known || (!resolver.limitOverall.exists && resolver.limitOverall.exhausted.known)
		return knownGuardBoolean(known)
	case "account.limits.exhausted":
		value := resolver.limitOverall.exhausted
		if !value.known {
			value.unknownFields = map[string]struct{}{identifier: {}}
		}
		return value
	case "account.limits.exhausted_known":
		return knownGuardBoolean(resolver.limitOverall.exhausted.known)
	case "account.limits.any":
		if resolver.limitOverall.exists {
			return knownGuardBoolean(true)
		}
		if resolver.limitOverall.exhausted.known {
			return knownGuardBoolean(false)
		}
		return unknownGuardValue(guardValueBoolean, identifier)
	}

	parts := strings.Split(identifier, ".")
	if len(parts) != 4 {
		return unknownGuardValue(kind, identifier)
	}
	aggregate := resolver.limitOverall
	if parts[2] != "any" {
		var exists bool
		aggregate, exists = resolver.limitWindows[normalizeRepositoryReviewGuardLimitWindow(parts[2])]
		if !exists {
			if parts[3] == "known" || parts[3] == "observed" {
				return knownGuardBoolean(false)
			}
			return unknownGuardValue(kind, identifier)
		}
	}
	switch parts[3] {
	case "known":
		return knownGuardBoolean(aggregate.known)
	case "observed":
		return knownGuardBoolean(aggregate.observed)
	case "remaining_percent":
		if aggregate.known {
			return knownGuardNumber(aggregate.remainingPercent)
		}
	case "used_percent":
		if aggregate.known {
			return knownGuardNumber(100 - aggregate.remainingPercent)
		}
	case "minimum_remaining_percent":
		if aggregate.observed {
			return knownGuardNumber(aggregate.remainingPercent)
		}
	case "maximum_used_percent":
		if aggregate.observed {
			return knownGuardNumber(100 - aggregate.remainingPercent)
		}
	}
	return unknownGuardValue(kind, identifier)
}

func normalizeRepositoryReviewGuardLimitWindow(window string) string {
	window = strings.ToLower(strings.TrimSpace(window))
	var normalized strings.Builder
	separator := false
	for index := 0; index < len(window); index++ {
		character := window[index]
		if isRepositoryReviewGuardIdentifierStart(character) || isRepositoryReviewGuardDigit(character) ||
			character == '-' {
			if separator && normalized.Len() > 0 {
				normalized.WriteByte('_')
			}
			separator = false
			normalized.WriteByte(character)
			continue
		}
		separator = true
	}
	return strings.Trim(normalized.String(), "_")
}

func repositoryReviewGuardExpressionUsesPrefix(expression, prefix string) bool {
	node, err := parseRepositoryReviewGuardExpression(expression)
	if err != nil || node == nil {
		return false
	}
	identifiers := make(map[string]struct{})
	node.collectIdentifiers(identifiers)
	for identifier := range identifiers {
		if identifier == prefix || strings.HasPrefix(identifier, prefix+".") {
			return true
		}
	}
	return false
}

func operatorText(node repositoryReviewGuardNode) string {
	switch typed := node.(type) {
	case *repositoryReviewGuardIdentifierNode:
		return typed.identifier
	case *repositoryReviewGuardLiteralNode:
		return typed.text
	default:
		return "value"
	}
}

func invalidRepositoryReviewGuardExpressionf(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRepositoryReviewGuardExpression, fmt.Sprintf(format, arguments...))
}

func invalidRepositoryReviewGuardEnvironmentf(format string, arguments ...any) error {
	return fmt.Errorf("repository review guard environment: %s", fmt.Sprintf(format, arguments...))
}
