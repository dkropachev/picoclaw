package main

import "testing"

func TestP015B2BPendingRetirementPreservesImmutableSource(t *testing.T) {
	pending := p015LoggingSite{
		ID:          "B001",
		Disposition: "b2b_deferred",
		File:        "pkg/agent/fixture.go",
		Owner:       p015ModulePath + "/pkg/agent.fixture",
		Ordinal:     1,
		Kind:        "pico_legacy",
		Callee:      "pico.WarnCF",
		Call:        `logger.WarnCF("agent", "fixture", nil)`,
		Canary:      "-",
	}
	retired := pending
	retired.Disposition = "b2b_retired"
	retired.Kind = "retired"
	retired.Canary = "pkg/agent/p015b2b_catalog_logging_test.go#" +
		"TestP015B2BCatalogLoggingASTManifest"

	if issues := p015LedgerHistoryIssues(
		[]p015LoggingSite{pending},
		[]p015LoggingSite{retired},
	); len(issues) != 0 {
		t.Fatalf("exact B pending-to-retired transition rejected: %v", issues)
	}

	mutations := map[string]func(*p015LoggingSite){
		"id":      func(row *p015LoggingSite) { row.ID = "B999" },
		"file":    func(row *p015LoggingSite) { row.File = "pkg/agent/other.go" },
		"owner":   func(row *p015LoggingSite) { row.Owner += ".other" },
		"ordinal": func(row *p015LoggingSite) { row.Ordinal++ },
		"callee":  func(row *p015LoggingSite) { row.Callee = "pico.ErrorCF" },
		"call":    func(row *p015LoggingSite) { row.Call += " /* changed */" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := retired
			mutate(&candidate)
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{pending},
				[]p015LoggingSite{candidate},
			); len(issues) == 0 {
				t.Fatalf("B pending-to-retired %s mutation was accepted", name)
			}
		})
	}
}
