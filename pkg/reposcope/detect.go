package reposcope

import (
	"bytes"
	"path"
	"strings"
)

var extensionLanguages = map[string]Language{
	".adb":     "ada",
	".ads":     "ada",
	".asm":     "assembly",
	".s":       "assembly",
	".c":       "c",
	".h":       "c",
	".cc":      "cpp",
	".cpp":     "cpp",
	".cxx":     "cpp",
	".hpp":     "cpp",
	".hh":      "cpp",
	".cs":      "csharp",
	".clj":     "clojure",
	".cljs":    "clojure",
	".dart":    "dart",
	".ex":      "elixir",
	".exs":     "elixir",
	".erl":     "erlang",
	".hrl":     "erlang",
	".fs":      "fsharp",
	".fsx":     "fsharp",
	".go":      "go",
	".groovy":  "groovy",
	".hs":      "haskell",
	".html":    "html",
	".htm":     "html",
	".java":    "java",
	".js":      "javascript",
	".mjs":     "javascript",
	".cjs":     "javascript",
	".jsx":     "javascript",
	".jsonnet": "jsonnet",
	".kt":      "kotlin",
	".kts":     "kotlin",
	".lua":     "lua",
	".m":       "objective-c",
	".mm":      "objective-cpp",
	".ml":      "ocaml",
	".mli":     "ocaml",
	".php":     "php",
	".pl":      "perl",
	".pm":      "perl",
	".ps1":     "powershell",
	".py":      "python",
	".pyi":     "python",
	".r":       "r",
	".rb":      "ruby",
	".rs":      "rust",
	".scala":   "scala",
	".sh":      "shell",
	".bash":    "shell",
	".zsh":     "shell",
	".fish":    "shell",
	".sol":     "solidity",
	".sql":     "sql",
	".swift":   "swift",
	".tf":      "terraform",
	".ts":      "typescript",
	".tsx":     "typescript",
	".vue":     "vue",
	".zig":     "zig",
	".css":     "css",
	".scss":    "scss",
	".sass":    "sass",
	".less":    "less",
	".proto":   "protobuf",
	".yaml":    "yaml",
	".yml":     "yaml",
	".toml":    "toml",
	".xml":     "xml",
}

var basenameLanguages = map[string]Language{
	"dockerfile": "dockerfile", "containerfile": "dockerfile", "makefile": "makefile", "gnumakefile": "makefile",
	"rakefile": "ruby", "gemfile": "ruby", "vagrantfile": "ruby", "jenkinsfile": "groovy",
}

func isKnownLanguage(language Language) bool {
	for _, known := range extensionLanguages {
		if language == known {
			return true
		}
	}
	for _, known := range basenameLanguages {
		if language == known {
			return true
		}
	}
	return false
}

// DetectLanguage identifies source languages by stable filename rules, then by
// a small set of shebangs for extensionless scripts.
func DetectLanguage(filePath string, sample []byte) (Language, bool) {
	base := strings.ToLower(path.Base(filePath))
	if language, ok := basenameLanguages[base]; ok {
		return language, true
	}
	if language, ok := extensionLanguages[strings.ToLower(path.Ext(base))]; ok {
		return language, true
	}
	firstLine := sample
	if index := bytes.IndexByte(firstLine, '\n'); index >= 0 {
		firstLine = firstLine[:index]
	}
	shebang := strings.ToLower(string(firstLine))
	if strings.HasPrefix(shebang, "#!") {
		switch {
		case strings.Contains(shebang, "python"):
			return "python", true
		case strings.Contains(shebang, "ruby"):
			return "ruby", true
		case strings.Contains(shebang, "node"), strings.Contains(shebang, "deno"):
			return "javascript", true
		case strings.Contains(shebang, "perl"):
			return "perl", true
		case strings.Contains(shebang, "bash"),
			strings.Contains(shebang, "zsh"),
			strings.Contains(shebang, "fish"),
			strings.Contains(shebang, "/sh"):
			return "shell", true
		}
	}
	return "", false
}

// ClassifyCodeType deterministically classifies source role. Benchmark rules
// precede ordinary test rules, and explicit hot-path names precede normal code.
func ClassifyCodeType(filePath string, language Language, sample []byte) CodeType {
	lowerPath := strings.ToLower(filePath)
	base := path.Base(lowerPath)
	parts := strings.Split(lowerPath, "/")
	if isBenchmarkPath(parts, base) || benchmarkMarker(language, sample) {
		return CodeTypeBenchTest
	}
	if isTestPath(parts, base) {
		return CodeTypeTest
	}
	if isHotpath(parts, base) {
		return CodeTypeHotpath
	}
	return CodeTypeCode
}

func isBenchmarkPath(parts []string, base string) bool {
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "bench", "benches", "benchmark", "benchmarks", "performance-tests", "perf-tests":
			return true
		}
	}
	return strings.Contains(base, "_bench.") || strings.Contains(base, ".bench.") ||
		strings.HasPrefix(base, "bench_") ||
		strings.HasPrefix(base, "benchmark_")
}

func benchmarkMarker(language Language, sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	lower := bytes.ToLower(sample)
	markers := [][]byte{[]byte("func benchmark"), []byte("@benchmark"), []byte("#[bench]"), []byte("criterion_group!")}
	if language == "java" || language == "kotlin" {
		markers = append(markers, []byte("org.openjdk.jmh"))
	}
	for _, marker := range markers {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTestPath(parts []string, base string) bool {
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "test", "tests", "testing", "spec", "specs", "__tests__", "integration-tests", "e2e":
			return true
		}
	}
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(base, "test_") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.HasSuffix(base, "test.java") ||
		strings.HasSuffix(base, "tests.java") ||
		strings.HasSuffix(base, "_test.rs") ||
		strings.HasSuffix(base, "_spec.rb")
}

func isHotpath(parts []string, base string) bool {
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "hotpath", "hotpaths", "hot-path", "hot_path", "critical-path", "critical_path":
			return true
		}
	}
	return strings.Contains(base, "_hotpath.") || strings.Contains(base, ".hotpath.") ||
		strings.Contains(base, "_critical_path.")
}

// DeriveLocation derives stable diversity buckets from repository layout.
// Region is the top-level directory and Module includes one additional
// directory where present. Root files use the literal "root" bucket.
func DeriveLocation(filePath string) (module, region string, err error) {
	filePath, err = normalizeFilePath(filePath)
	if err != nil {
		return "", "", err
	}
	directory := path.Dir(filePath)
	if directory == "." {
		return "root", "root", nil
	}
	parts := strings.Split(directory, "/")
	region = parts[0]
	module = region
	if len(parts) > 1 {
		module += "/" + parts[1]
	}
	return module, region, nil
}
