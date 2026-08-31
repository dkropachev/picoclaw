package api

import (
	"net/url"
	"runtime"
	"testing"
)

func TestPreexistingWeComURLBuildersCoverageCloseout(t *testing.T) {
	generated, err := buildWecomQRGenerateURL(
		"https://example.test/generate?existing=1",
		"source with spaces",
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	parsedGenerate, err := url.Parse(generated)
	if err != nil {
		t.Fatal(err)
	}
	generateQuery := parsedGenerate.Query()
	if generateQuery.Get("existing") != "1" ||
		generateQuery.Get("source") != "source with spaces" ||
		generateQuery.Get("sourceID") != "source with spaces" ||
		generateQuery.Get("plat") != "3" {
		t.Fatalf("generate URL=%q query=%v", generated, generateQuery)
	}
	if _, generateErr := buildWecomQRGenerateURL("%", "source", 3); generateErr == nil {
		t.Fatal("invalid generate URL was accepted")
	}

	queried, err := buildWecomQRQueryURL(
		"https://example.test/query?existing=1",
		"scode with spaces",
	)
	if err != nil {
		t.Fatal(err)
	}
	parsedQuery, err := url.Parse(queried)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedQuery.Query()
	if query.Get("existing") != "1" || query.Get("scode") != "scode with spaces" {
		t.Fatalf("query URL=%q query=%v", queried, query)
	}
	if _, queryErr := buildWecomQRQueryURL("%", "scode"); queryErr == nil {
		t.Fatal("invalid query URL was accepted")
	}
}

func TestPreexistingWeComPlatformCodeCoverageCloseout(t *testing.T) {
	want := 0
	switch runtime.GOOS {
	case "darwin":
		want = 1
	case "windows":
		want = 2
	case "linux":
		want = 3
	}
	if got := wecomPlatformCode(); got != want {
		t.Fatalf("platform code=%d want=%d for %s", got, want, runtime.GOOS)
	}
}

func TestPreexistingWorkflowTriggerMapCloneCoverageCloseout(t *testing.T) {
	source := map[string]string{"one": "first", "two": "second"}
	cloned := cloneWorkflowTriggerStringMap(source)
	source["one"] = "changed"
	if cloned["one"] != "first" || cloned["two"] != "second" {
		t.Fatalf("cloned map=%v", cloned)
	}
}
