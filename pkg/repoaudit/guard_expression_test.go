package repoaudit

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRepositoryReviewGuardExpressionPrecedenceAndTypes(t *testing.T) {
	t.Parallel()
	environment := RepositoryReviewGuardEnvironment{
		SpentTokens: RepositoryReviewTokenUsage{
			PromptTokens: 70, CompletionTokens: 30, CachedTokens: 10, TotalTokens: 100,
		},
		SpendTotalUSD:      1.25,
		CostKnown:          true,
		AccountLimitsKnown: true,
	}
	tests := []struct {
		name       string
		expression string
		want       bool
	}{
		{name: "empty allows", expression: "", want: true},
		{name: "whitespace allows", expression: " \n\t ", want: true},
		{name: "and binds before or", expression: "TRUE OR false AnD false", want: true},
		{name: "parentheses override", expression: "(true OR false) AND false", want: false},
		{name: "not binds first", expression: "NOT false AND spent.tokens.total == 100", want: true},
		{name: "single equals", expression: "spent.tokens.prompt = 70", want: true},
		{name: "not equal", expression: "spent.tokens.cached != 11", want: true},
		{name: "numeric comparisons", expression: "1 < 2 AND 2 <= 2 AND 3 > 2 AND 3 >= 3", want: true},
		{name: "quoted strings", expression: `'alpha' < "beta" AND "same" = 'same'`, want: true},
		{name: "boolean comparison", expression: "account.limits.known = true", want: true},
		{name: "false result", expression: "spent.tokens.completion > 30 OR false", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateRepositoryReviewGuardExpression(test.expression); err != nil {
				t.Fatalf("ValidateRepositoryReviewGuardExpression() error = %v", err)
			}
			got, err := EvaluateRepositoryReviewGuardExpression(test.expression, environment)
			if err != nil {
				t.Fatalf("EvaluateRepositoryReviewGuardExpression() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("EvaluateRepositoryReviewGuardExpression() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRepositoryReviewGuardExpressionUnknownIsFailClosed(t *testing.T) {
	t.Parallel()
	environment := RepositoryReviewGuardEnvironment{}
	tests := []struct {
		name       string
		expression string
		wantError  bool
		want       bool
		wantField  string
	}{
		{
			name: "unknown monetary spend", expression: "spend.total.usd < 5",
			wantError: true, wantField: "spend.total.usd",
		},
		{
			name: "true dominates unknown or", expression: "spend.total.usd < 5 OR true",
			want: true,
		},
		{
			name: "false dominates unknown and", expression: "spend.total.usd < 5 AND false",
			want: false,
		},
		{
			name: "unknown account-limit absence", expression: "account.limits.any",
			wantError: true, wantField: "account.limits.any",
		},
		{
			name: "known flag can be tested", expression: "account.limits.known = false",
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := EvaluateRepositoryReviewGuardExpression(test.expression, environment)
			if test.wantError {
				if got {
					t.Fatal("unknown expression permitted work")
				}
				if !errors.Is(err, ErrRepositoryReviewGuardUnknown) {
					t.Fatalf("error = %v, want ErrRepositoryReviewGuardUnknown", err)
				}
				var unknown *RepositoryReviewGuardUnknownError
				if !errors.As(err, &unknown) {
					t.Fatalf("error %T is not RepositoryReviewGuardUnknownError", err)
				}
				if len(unknown.Fields) != 1 || unknown.Fields[0] != test.wantField {
					t.Fatalf("unknown fields = %v, want [%s]", unknown.Fields, test.wantField)
				}
				return
			}
			if err != nil {
				t.Fatalf("EvaluateRepositoryReviewGuardExpression() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("EvaluateRepositoryReviewGuardExpression() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRepositoryReviewGuardExpressionBoundsAndInvalidSyntax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		expression string
		contains   string
	}{
		{name: "unsupported field", expression: "account.name = 'default'", contains: "unsupported guard field"},
		{name: "wildcard", expression: "account.limits.*.known = true", contains: "wildcards are not supported"},
		{name: "literal family wildcard", expression: "spent.tokens.* < 1", contains: "wildcards are not supported"},
		{name: "mismatched types", expression: "spent.tokens.total = true", contains: "mismatched operand types"},
		{name: "ordered boolean", expression: "true < false", contains: "boolean values support only"},
		{name: "bare number", expression: "123", contains: "must be compared"},
		{name: "unterminated string", expression: `"hello`, contains: "unterminated string"},
		{name: "missing right parenthesis", expression: "(true OR false", contains: "expected ')'"},
		{name: "trailing operator", expression: "true AND", contains: "expected an identifier or literal"},
		{
			name:       "too many bytes",
			expression: strings.Repeat(" ", maxRepositoryReviewGuardExpressionBytes+1),
			contains:   "exceeds 4096 bytes",
		},
		{
			name: "too many tokens", expression: strings.Repeat("true AND ", 128) + "true",
			contains: "exceeds 256 tokens",
		},
		{
			name: "too deeply nested", expression: strings.Repeat("(", 17) + "true" + strings.Repeat(")", 17),
			contains: "nesting exceeds 16",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRepositoryReviewGuardExpression(test.expression)
			if !errors.Is(err, ErrInvalidRepositoryReviewGuardExpression) {
				t.Fatalf("error = %v, want ErrInvalidRepositoryReviewGuardExpression", err)
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %q, want substring %q", err, test.contains)
			}
		})
	}

	validDepth := strings.Repeat("(", maxRepositoryReviewGuardExpressionDepth) + "true" +
		strings.Repeat(")", maxRepositoryReviewGuardExpressionDepth)
	if err := ValidateRepositoryReviewGuardExpression(validDepth); err != nil {
		t.Fatalf("exact nesting limit rejected: %v", err)
	}
	exactBytes := "true" + strings.Repeat(" ", maxRepositoryReviewGuardExpressionBytes-len("true"))
	if err := ValidateRepositoryReviewGuardExpression(exactBytes); err != nil {
		t.Fatalf("exact byte limit rejected: %v", err)
	}
	exactTokens := "NOT " + strings.Repeat("true AND ", 127) + "true"
	if err := ValidateRepositoryReviewGuardExpression(exactTokens); err != nil {
		t.Fatalf("exact token limit rejected: %v", err)
	}
}

func TestRepositoryReviewGuardExpressionWeeklyExample(t *testing.T) {
	t.Parallel()
	weekly := 42.5
	environment := RepositoryReviewGuardEnvironment{
		SpentTokens: RepositoryReviewTokenUsage{
			PromptTokens: 8000, CompletionTokens: 1500, CachedTokens: 1000, TotalTokens: 9500,
		},
		SpendTotalUSD:      3.75,
		CostKnown:          true,
		AccountLimitsKnown: true,
		AccountLimitSnapshots: []RepositoryReviewAccountLimitSnapshot{
			{AccountID: "default", Window: "Weekly", RemainingPercent: &weekly},
		},
	}
	expression := `spent.tokens.total < 10000 AND spend.total.usd <= 4 AND ` +
		`account.limits.weekly.known AND account.limits.weekly.remaining_percent >= 40`
	allowed, err := EvaluateRepositoryReviewGuardExpression(expression, environment)
	if err != nil {
		t.Fatalf("EvaluateRepositoryReviewGuardExpression() error = %v", err)
	}
	if !allowed {
		t.Fatal("weekly guard unexpectedly denied work")
	}

	denied, err := EvaluateRepositoryReviewGuardExpression(
		`account.limits.weekly.remaining_percent >= 50`, environment,
	)
	if err != nil {
		t.Fatalf("denying weekly expression returned error: %v", err)
	}
	if denied {
		t.Fatal("weekly guard permitted work below its threshold")
	}
}

func TestRepositoryReviewGuardExpressionAggregatesLimitsConservatively(t *testing.T) {
	t.Parallel()
	weeklyHigh, weeklyLow, daily := 75.0, 25.0, 60.0
	environment := RepositoryReviewGuardEnvironment{
		AccountLimitsKnown: true,
		AccountLimitSnapshots: []RepositoryReviewAccountLimitSnapshot{
			{AccountID: "account-a", Name: "primary", Window: "Weekly", RemainingPercent: &weeklyHigh},
			{AccountID: "account-b", Name: "secondary", Window: "weekly", RemainingPercent: &weeklyLow},
			{AccountID: "account-a", Name: "primary", Window: "Daily Reset", RemainingPercent: &daily},
		},
	}
	tests := []struct {
		expression string
		want       bool
	}{
		{expression: "account.limits.any", want: true},
		{expression: "account.limits.known", want: true},
		{expression: "account.limits.exhausted", want: false},
		{expression: "account.limits.weekly.remaining_percent = 25", want: true},
		{expression: "account.limits.weekly.used_percent = 75", want: true},
		{expression: "account.limits.any.remaining_percent = 25", want: true},
		{expression: "account.limits.any.used_percent = 75", want: true},
		{expression: "account.limits.daily_reset.remaining_percent = 60", want: true},
		{expression: "account.limits.monthly.known = false", want: true},
	}
	for _, test := range tests {
		allowed, err := EvaluateRepositoryReviewGuardExpression(test.expression, environment)
		if err != nil {
			t.Fatalf("%q error = %v", test.expression, err)
		}
		if allowed != test.want {
			t.Fatalf("%q = %t, want %t", test.expression, allowed, test.want)
		}
	}

	unknown := environment
	unknown.AccountLimitSnapshots = append(
		append([]RepositoryReviewAccountLimitSnapshot(nil), environment.AccountLimitSnapshots...),
		RepositoryReviewAccountLimitSnapshot{AccountID: "account-c", Window: "weekly"},
	)
	allowed, err := EvaluateRepositoryReviewGuardExpression(
		"account.limits.weekly.known = false", unknown,
	)
	if err != nil || !allowed {
		t.Fatalf("unknown weekly known flag = %t, %v", allowed, err)
	}
	if allowed, err = EvaluateRepositoryReviewGuardExpression(
		"account.limits.weekly.remaining_percent >= 1", unknown,
	); allowed || !errors.Is(err, ErrRepositoryReviewGuardUnknown) {
		t.Fatalf("partially unknown aggregate = %t, %v", allowed, err)
	}
	if allowed, err = EvaluateRepositoryReviewGuardExpression(
		"account.limits.weekly.observed and account.limits.weekly.minimum_remaining_percent = 25 and account.limits.weekly.maximum_used_percent = 75",
		unknown,
	); err != nil || !allowed {
		t.Fatalf("partial conservative observations = %t, %v", allowed, err)
	}

	exhausted := 0.0
	unknown.AccountLimitSnapshots = append(unknown.AccountLimitSnapshots,
		RepositoryReviewAccountLimitSnapshot{AccountID: "account-d", Window: "daily", RemainingPercent: &exhausted},
	)
	if allowed, err = EvaluateRepositoryReviewGuardExpression(
		"account.limits.exhausted",
		unknown,
	); err != nil ||
		!allowed {
		t.Fatalf("decisively exhausted aggregate = %t, %v", allowed, err)
	}

	incomplete := environment
	incomplete.AccountLimitsKnown = false
	if allowed, err = EvaluateRepositoryReviewGuardExpression(
		"account.limits.weekly.remaining_percent >= 1", incomplete,
	); allowed || !errors.Is(err, ErrRepositoryReviewGuardUnknown) {
		t.Fatalf("incomplete snapshot set = %t, %v", allowed, err)
	}
}

func TestRepositoryReviewGuardExpressionUsageHelpers(t *testing.T) {
	t.Parallel()
	if !RepositoryReviewGuardUsesAccountLimits("account.limits.weekly.remaining_percent > 10") {
		t.Fatal("UsesAccountLimits() missed an account-limit field")
	}
	if RepositoryReviewGuardUsesAccountLimits("spent.tokens.total < 100") {
		t.Fatal("UsesAccountLimits() matched token accounting")
	}
	if !RepositoryReviewGuardUsesSpend("spend.total.usd < 10") {
		t.Fatal("UsesSpend() missed monetary spend")
	}
	if RepositoryReviewGuardUsesSpend("spent.tokens.total < 100") {
		t.Fatal("UsesSpend() matched token accounting")
	}
	if RepositoryReviewGuardUsesSpend("spend.total.* < 10") {
		t.Fatal("UsesSpend() accepted an invalid wildcard expression")
	}
}

func TestRepositoryReviewGuardExpressionRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()
	_, err := EvaluateRepositoryReviewGuardExpression("spent.tokens.total < 10", RepositoryReviewGuardEnvironment{
		SpentTokens: RepositoryReviewTokenUsage{PromptTokens: -1},
	})
	if err == nil || !strings.Contains(err.Error(), "token counters must be non-negative") {
		t.Fatalf("invalid token environment error = %v", err)
	}

	invalidPercent := 101.0
	_, err = EvaluateRepositoryReviewGuardExpression("account.limits.known", RepositoryReviewGuardEnvironment{
		AccountLimitsKnown: true,
		AccountLimitSnapshots: []RepositoryReviewAccountLimitSnapshot{
			{AccountID: "default", Window: "weekly", RemainingPercent: &invalidPercent},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid remaining percentage") {
		t.Fatalf("invalid account-limit environment error = %v", err)
	}
}

func TestRepositoryReviewGuardExpressionScannerAndParserBoundaries(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		expression string
		contains   string
	}{
		{expression: "! true", contains: "expected '=' after '!'"},
		{expression: "@", contains: "unexpected character"},
		{expression: "'line\nbreak' = 'x'", contains: "unescaped newline"},
		{expression: `'trailing\`, contains: "unterminated escape"},
		{expression: `'bad\x'`, contains: "unsupported escape"},
		{expression: ". = 0", contains: "invalid number"},
		{expression: "1e = 1", contains: "invalid exponent"},
		{expression: "1e+ = 1", contains: "invalid exponent"},
		{expression: "1e9999 = 1", contains: "invalid finite number"},
		{expression: "spent..tokens = 1", contains: "dotted identifiers"},
		{expression: "spent.tokens. = 1", contains: "dotted identifiers"},
		{expression: "true false", contains: "unexpected token"},
		{expression: "true OR", contains: "expected an identifier or literal"},
		{
			expression: strings.Repeat("NOT ", maxRepositoryReviewGuardExpressionDepth+1) + "true",
			contains:   "nesting exceeds",
		},
	}
	for _, test := range invalid {
		err := ValidateRepositoryReviewGuardExpression(test.expression)
		if err == nil || !strings.Contains(err.Error(), test.contains) {
			t.Errorf(
				"ValidateRepositoryReviewGuardExpression(%q) error = %v, want %q",
				test.expression,
				err,
				test.contains,
			)
		}
	}
	if _, err := repositoryReviewGuardIdentifierKind("account.limits.Weekly.known"); err == nil ||
		!strings.Contains(err.Error(), "is not normalized") {
		t.Fatalf("non-normalized internal identifier error = %v", err)
	}

	allowed, err := EvaluateRepositoryReviewGuardExpression(
		`"escaped\\quote\"apostrophe\'newline\nreturn\rtab\tback\bform\f" != '' AND +.5e+2 = 50 AND -.5E-2 < 0`,
		RepositoryReviewGuardEnvironment{},
	)
	if err != nil || !allowed {
		t.Fatalf("escaped strings and signed exponents = %t, %v", allowed, err)
	}

	// Every punctuation token has its own bounded append path. Fill the token
	// budget first, then assert that the next token is rejected consistently.
	for _, suffix := range []string{"(", ")", "=", "!=", "<", ">", `'x'`, "1"} {
		expression := strings.Repeat("true AND ", maxRepositoryReviewGuardExpressionTokens/2) + suffix
		err := ValidateRepositoryReviewGuardExpression(expression)
		if err == nil || !strings.Contains(err.Error(), "exceeds 256 tokens") {
			t.Errorf("token overflow suffix %q error = %v", suffix, err)
		}
	}
}

func TestRepositoryReviewGuardExpressionNodeContractAndEnvironmentBoundaries(t *testing.T) {
	t.Parallel()

	node, err := parseRepositoryReviewGuardExpression(
		"NOT (account.limits.known AND spent.tokens.total < 10)",
	)
	if err != nil {
		t.Fatal(err)
	}
	if node.valueKind() != guardValueBoolean {
		t.Fatalf("logical node kind = %v", node.valueKind())
	}
	identifiers := make(map[string]struct{})
	node.collectIdentifiers(identifiers)
	for _, expected := range []string{"account.limits.known", "spent.tokens.total"} {
		if _, found := identifiers[expected]; !found {
			t.Fatalf("identifiers = %#v, missing %q", identifiers, expected)
		}
	}

	comparison, err := parseRepositoryReviewGuardExpression("false != true")
	if err != nil || comparison.valueKind() != guardValueBoolean {
		t.Fatalf("comparison contract kind=%v err=%v", comparison.valueKind(), err)
	}
	comparisonResult := comparison.evaluate(&repositoryReviewGuardResolver{})
	if !comparisonResult.known || !comparisonResult.boolean {
		t.Fatalf("false-first boolean comparison = %#v", comparisonResult)
	}
	if got := (&RepositoryReviewGuardUnknownError{}).Error(); got != ErrRepositoryReviewGuardUnknown.Error() {
		t.Fatalf("empty unknown error = %q", got)
	}
	var nilUnknown *RepositoryReviewGuardUnknownError
	if got := nilUnknown.Error(); got != ErrRepositoryReviewGuardUnknown.Error() {
		t.Fatalf("nil unknown error = %q", got)
	}
	trueFirst, err := parseRepositoryReviewGuardExpression("true != false")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := newRepositoryReviewGuardResolver(RepositoryReviewGuardEnvironment{})
	if err != nil {
		t.Fatal(err)
	}
	if result := trueFirst.evaluate(resolver); !result.known || !result.boolean {
		t.Fatalf("true-first boolean comparison = %#v", result)
	}
	logical := &repositoryReviewGuardLogicalNode{
		operator: guardTokenAnd,
		left:     &repositoryReviewGuardLiteralNode{value: knownGuardBoolean(true), text: "true"},
		right:    &repositoryReviewGuardLiteralNode{value: knownGuardBoolean(true), text: "true"},
	}
	if logical.valueKind() != guardValueBoolean {
		t.Fatalf("logical value kind = %v", logical.valueKind())
	}
	if got := operatorText(
		&repositoryReviewGuardIdentifierNode{identifier: "spent.tokens.total"},
	); got != "spent.tokens.total" {
		t.Fatalf("identifier operator text = %q", got)
	}
	if got := operatorText(logical); got != "value" {
		t.Fatalf("composite operator text = %q", got)
	}
	if value := resolver.resolve("unsupported", guardValueNumber); value.known || value.kind != guardValueNumber {
		t.Fatalf("unsupported resolver value = %#v", value)
	}
	malformedNumberParser := &repositoryReviewGuardParser{tokens: []repositoryReviewGuardToken{
		{kind: guardTokenNumber, text: "not-a-number"}, {kind: guardTokenEOF},
	}}
	if _, operandErr := malformedNumberParser.parseOperand(); operandErr == nil ||
		!strings.Contains(operandErr.Error(), "invalid number") {
		t.Fatalf("malformed internal number error = %v", operandErr)
	}
	if wildcardErr := ValidateRepositoryReviewGuardExpression("*"); wildcardErr == nil ||
		!strings.Contains(wildcardErr.Error(), "wildcards are not supported") {
		t.Fatalf("bare wildcard error = %v", wildcardErr)
	}

	boundaryEnvironments := []struct {
		name        string
		environment RepositoryReviewGuardEnvironment
		contains    string
	}{
		{
			name: "token overflow",
			environment: RepositoryReviewGuardEnvironment{SpentTokens: RepositoryReviewTokenUsage{
				PromptTokens: math.MaxInt64, CompletionTokens: 1, TotalTokens: math.MaxInt64,
			}},
			contains: "overflow",
		},
		{
			name: "inconsistent total",
			environment: RepositoryReviewGuardEnvironment{SpentTokens: RepositoryReviewTokenUsage{
				PromptTokens: 2, CompletionTokens: 2, TotalTokens: 3,
			}},
			contains: "inconsistent",
		},
		{
			name: "cached exceeds prompt",
			environment: RepositoryReviewGuardEnvironment{SpentTokens: RepositoryReviewTokenUsage{
				PromptTokens: 1, CachedTokens: 2, TotalTokens: 1,
			}},
			contains: "inconsistent",
		},
		{
			name:        "negative cost",
			environment: RepositoryReviewGuardEnvironment{CostKnown: true, SpendTotalUSD: -1},
			contains:    "finite and non-negative",
		},
		{
			name:        "infinite cost",
			environment: RepositoryReviewGuardEnvironment{CostKnown: true, SpendTotalUSD: math.Inf(1)},
			contains:    "finite and non-negative",
		},
		{
			name: "empty limit window",
			environment: RepositoryReviewGuardEnvironment{AccountLimitSnapshots: []RepositoryReviewAccountLimitSnapshot{
				{Window: "  "},
			}},
			contains: "empty window",
		},
	}
	for _, test := range boundaryEnvironments {
		_, evaluationErr := EvaluateRepositoryReviewGuardExpression("true", test.environment)
		if evaluationErr == nil || !strings.Contains(evaluationErr.Error(), test.contains) {
			t.Errorf("%s error = %v, want %q", test.name, evaluationErr, test.contains)
		}
	}
	allowed, err := EvaluateRepositoryReviewGuardExpression(
		"spent.tokens.total = 3",
		RepositoryReviewGuardEnvironment{SpentTokens: RepositoryReviewTokenUsage{
			PromptTokens: 2, CompletionTokens: 1,
		}},
	)
	if err != nil || !allowed {
		t.Fatalf("derived total token count = %t, %v", allowed, err)
	}

	// An absent but successfully enumerated limit set is known to be empty.
	allowed, err = EvaluateRepositoryReviewGuardExpression(
		"account.limits.known AND account.limits.exhausted_known AND NOT account.limits.any",
		RepositoryReviewGuardEnvironment{AccountLimitsKnown: true},
	)
	if err != nil || !allowed {
		t.Fatalf("known empty limit set = %t, %v", allowed, err)
	}
}

func FuzzRepositoryReviewGuardExpressionParserDoesNotPanic(f *testing.F) {
	f.Add("")
	f.Add("spent.tokens.total < 1000 AND account.limits.weekly.remaining_percent >= 20")
	f.Add("account.limits.*.known")
	f.Add(strings.Repeat("(", 32))
	environment := RepositoryReviewGuardEnvironment{AccountLimitsKnown: true}
	f.Fuzz(func(t *testing.T, expression string) {
		_ = ValidateRepositoryReviewGuardExpression(expression)
		_, _ = EvaluateRepositoryReviewGuardExpression(expression, environment)
	})
}
