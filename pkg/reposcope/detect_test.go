package reposcope

import "testing"

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path   string
		sample string
		want   Language
	}{
		{"main.go", "", "go"},
		{"web/App.TSX", "", "typescript"},
		{"contracts/token.sol", "", "solidity"},
		{"Dockerfile", "", "dockerfile"},
		{"GNUmakefile", "", "makefile"},
		{"Jenkinsfile", "", "groovy"},
		{"tool", "#!/usr/bin/env python3\n", "python"},
		{"tool", "#!/usr/bin/env ruby\n", "ruby"},
		{"tool", "#!/usr/bin/env node\n", "javascript"},
		{"tool", "#!/usr/bin/perl\n", "perl"},
		{"tool", "#!/bin/zsh\n", "shell"},
	}
	for _, test := range tests {
		got, ok := DetectLanguage(test.path, []byte(test.sample))
		if !ok || got != test.want {
			t.Errorf("DetectLanguage(%q) = %q, %v; want %q", test.path, got, ok, test.want)
		}
		if !isKnownLanguage(test.want) {
			t.Errorf("detected language %q is not known", test.want)
		}
	}
	if language, ok := DetectLanguage("README.unknown", []byte("plain text")); ok || language != "" {
		t.Fatalf("unknown detection = %q, %v", language, ok)
	}
	if language, ok := DetectLanguage("tool", []byte("#!/usr/bin/env unsupported\n")); ok || language != "" {
		t.Fatalf("unsupported shebang = %q, %v", language, ok)
	}
	if isKnownLanguage("brainfuck") {
		t.Fatal("unknown language accepted")
	}
}

func TestClassifyCodeType(t *testing.T) {
	tests := []struct {
		path     string
		language Language
		sample   string
		want     CodeType
	}{
		{"benchmarks/codec.go", "go", "", CodeTypeBenchTest},
		{"src/codec_bench.rs", "rust", "", CodeTypeBenchTest},
		{"src/codec_test.go", "go", "func TestCodec(t *testing.T) {}", CodeTypeTest},
		{"src/codec.go", "go", "func BenchmarkCodec(b *testing.B) {}", CodeTypeBenchTest},
		{"src/Codec.java", "java", "import org.openjdk.jmh.annotations.Benchmark;", CodeTypeBenchTest},
		{"tests/parser.py", "python", "", CodeTypeTest},
		{"src/parser.spec.ts", "typescript", "", CodeTypeTest},
		{"hot-path/parser.go", "go", "", CodeTypeHotpath},
		{"src/parser_hotpath.go", "go", "", CodeTypeHotpath},
		{"src/parser.go", "go", "benchmark(value)", CodeTypeCode},
	}
	for _, test := range tests {
		if got := ClassifyCodeType(test.path, test.language, []byte(test.sample)); got != test.want {
			t.Errorf("ClassifyCodeType(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	if benchmarkMarker("go", nil) {
		t.Fatal("empty source classified as benchmark")
	}
}

func TestDeriveLocation(t *testing.T) {
	tests := []struct {
		path   string
		module string
		region string
	}{
		{"main.go", "root", "root"},
		{"pkg/parser.go", "pkg", "pkg"},
		{"services/api/handler.go", "services/api", "services"},
		{"apps/web/src/index.ts", "apps/web", "apps"},
	}
	for _, test := range tests {
		module, region, err := DeriveLocation(test.path)
		if err != nil || module != test.module || region != test.region {
			t.Errorf("DeriveLocation(%q) = %q, %q, %v", test.path, module, region, err)
		}
	}
	if _, _, err := DeriveLocation("../outside.go"); err == nil {
		t.Fatal("unsafe location path accepted")
	}
}
