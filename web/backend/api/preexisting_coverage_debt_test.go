package api

import (
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestPreexistingWeixinFlowLifecycleCoverageCloseout(t *testing.T) {
	handler := &Handler{weixinFlows: make(map[string]*weixinFlow)}
	now := time.Now()
	flow := &weixinFlow{
		ID: "wx-flow", Status: weixinStatusWait,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	handler.storeWeixinFlow(flow)
	stored, found := handler.getWeixinFlow(flow.ID)
	if !found || stored.Status != weixinStatusWait {
		t.Fatalf("stored flow=%#v found=%v", stored, found)
	}
	stored.Status = "detached-copy"
	if current, _ := handler.getWeixinFlow(flow.ID); current.Status != weixinStatusWait {
		t.Fatalf("stored flow was mutated through its projection: %#v", current)
	}

	handler.updateWeixinFlowStatus(flow.ID, weixinStatusScanned)
	handler.setWeixinFlowConfirmed(flow.ID, "account-id")
	handler.setWeixinFlowError(flow.ID, "safe error")
	terminal, found := handler.getWeixinFlow(flow.ID)
	if !found || terminal.Status != weixinStatusError || terminal.AccountID != "account-id" ||
		terminal.Error != "safe error" {
		t.Fatalf("terminal flow=%#v found=%v", terminal, found)
	}

	expired := &weixinFlow{
		ID: "wx-expired", Status: weixinStatusWait,
		UpdatedAt: now, ExpiresAt: now.Add(-time.Minute),
	}
	stale := &weixinFlow{
		ID: "wx-stale", Status: weixinStatusError,
		UpdatedAt: now.Add(-weixinFlowGCAge - time.Minute),
	}
	handler.weixinFlows[expired.ID] = expired
	handler.weixinFlows[stale.ID] = stale
	handler.weixinMu.Lock()
	handler.gcWeixinFlowsLocked(now)
	handler.weixinMu.Unlock()
	if expired.Status != weixinStatusExpired {
		t.Fatalf("expired flow=%#v", expired)
	}
	if _, found := handler.weixinFlows[stale.ID]; found {
		t.Fatal("stale terminal flow was not collected")
	}
	if _, found := handler.getWeixinFlow("missing"); found {
		t.Fatal("missing flow was returned")
	}
	if id := newWeixinFlowID(); !strings.HasPrefix(id, "wx_") {
		t.Fatalf("flow ID=%q", id)
	}
}
