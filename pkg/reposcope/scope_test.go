package reposcope

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeScopeAndAllows(t *testing.T) {
	scope, err := NormalizeScope(Scope{
		IncludePrefixes: []string{" services/api/ ", "src/", "src"},
		ExcludePrefixes: []string{"src/generated/", "services/api/private"},
		CodeTypes:       []CodeType{CodeTypeTest, CodeTypeCode, CodeTypeTest},
		FreeText:        "focus on parsers, and include anything else you find",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(scope.IncludePrefixes, ","), "services/api,src"; got != want {
		t.Fatalf("includes = %q, want %q", got, want)
	}
	if got, want := strings.Join(scope.ExcludePrefixes, ","), "services/api/private,src/generated"; got != want {
		t.Fatalf("excludes = %q, want %q", got, want)
	}
	if len(scope.CodeTypes) != 2 || scope.CodeTypes[0] != CodeTypeCode || scope.CodeTypes[1] != CodeTypeTest {
		t.Fatalf("normalized code types = %#v", scope.CodeTypes)
	}

	tests := []struct {
		path     string
		codeType CodeType
		want     bool
	}{
		{"src/main.go", CodeTypeCode, true},
		{"src2/main.go", CodeTypeCode, false},
		{"src/generated/model.go", CodeTypeCode, false},
		{"services/api/handler_test.go", CodeTypeTest, true},
		{"services/api/private/key.go", CodeTypeCode, false},
		{"src/bench_test.go", CodeTypeBenchTest, false},
	}
	for _, test := range tests {
		got, allowErr := scope.Allows(test.path, test.codeType)
		if allowErr != nil {
			t.Fatalf("Allows(%q): %v", test.path, allowErr)
		}
		if got != test.want {
			t.Errorf("Allows(%q, %q) = %v, want %v", test.path, test.codeType, got, test.want)
		}
	}

	// Free text can ask for a wider scope, but cannot grant it.
	widening := Scope{IncludePrefixes: []string{"src"}, FreeText: "also include secrets/"}
	if allowed, allowErr := widening.Allows("secrets/token.go", CodeTypeCode); allowErr != nil || allowed {
		t.Fatalf("free text widened hard scope: allowed=%v err=%v", allowed, allowErr)
	}
	root, err := NormalizeScope(Scope{IncludePrefixes: []string{"", ".", "./"}})
	if err != nil || len(root.IncludePrefixes) != 1 || root.IncludePrefixes[0] != "." {
		t.Fatalf("root normalization = %#v, %v", root, err)
	}
	if allowed, err := root.Allows("anywhere/file.rs", CodeTypeHotpath); err != nil || !allowed {
		t.Fatalf("root scope denied file: allowed=%v err=%v", allowed, err)
	}
}

func TestScopeValidationRejectsUnsafeInputs(t *testing.T) {
	tooMany := make([]string, MaxScopePrefixes+1)
	for index := range tooMany {
		tooMany[index] = "src"
	}
	badUTF8 := string([]byte{0xff})
	tests := []Scope{
		{IncludePrefixes: []string{"/absolute"}},
		{IncludePrefixes: []string{"../escape"}},
		{ExcludePrefixes: []string{"../escape"}},
		{IncludePrefixes: []string{"a/../escape"}},
		{IncludePrefixes: []string{"./src"}},
		{IncludePrefixes: []string{`src\windows`}},
		{IncludePrefixes: []string{"src\nother"}},
		{IncludePrefixes: []string{strings.Repeat("x", MaxRepositoryPathBytes+1)}},
		{IncludePrefixes: []string{badUTF8}},
		{IncludePrefixes: tooMany},
		{ExcludePrefixes: tooMany},
		{CodeTypes: []CodeType{"documentation"}},
		{FreeText: strings.Repeat("x", MaxFreeTextBytes+1)},
		{FreeText: badUTF8},
		{FreeText: "bad\x00text"},
		{FreeText: "bad\x01text"},
	}
	for index, scope := range tests {
		if _, err := NormalizeScope(scope); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("case %d: error = %v, want ErrInvalidScope", index, err)
		}
	}

	if _, err := (Scope{}).Allows("../escape.go", CodeTypeCode); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unsafe path error = %v", err)
	}
	if _, err := (Scope{}).Allows("main.go", "unknown"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("unknown type error = %v", err)
	}
	if _, err := (Scope{CodeTypes: []CodeType{"bad"}}).Allows(
		"main.go",
		CodeTypeCode,
	); !errors.Is(
		err,
		ErrInvalidScope,
	) {
		t.Fatalf("invalid scope error = %v", err)
	}
}

func TestNormalizeFilePathAndPrefixHelpers(t *testing.T) {
	badPaths := []string{
		"",
		".",
		"/a",
		"./a",
		"a/../b",
		"a/",
		`a\b`,
		"a\nb",
		strings.Repeat("p", MaxRepositoryPathBytes+1),
		string([]byte{0xff}),
	}
	for _, value := range badPaths {
		if _, err := normalizeFilePath(value); err == nil {
			t.Errorf("normalizeFilePath(%q) unexpectedly succeeded", value)
		}
	}
	if got, err := normalizeFilePath("src/a.go"); err != nil || got != "src/a.go" {
		t.Fatalf("normalizeFilePath valid = %q, %v", got, err)
	}
	if !hasPathPrefix("src/a.go", "src") || !hasPathPrefix("src", "src") || !hasPathPrefix("x", ".") ||
		hasPathPrefix("src2/a.go", "src") {
		t.Fatal("segment-aware prefix matching failed")
	}
	if !containsCodeType([]CodeType{CodeTypeCode}, CodeTypeCode) || containsCodeType(nil, CodeTypeCode) {
		t.Fatal("code type membership failed")
	}
}
