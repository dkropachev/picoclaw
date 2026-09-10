package sqliteprovider

import (
	"context"
	"database/sql/driver"
	"errors"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func immutableGenerationSourceForTest(
	use func(context.Context, func(context.Context, string) error) error,
) ImmutableGenerationSource {
	source, err := NewImmutableGenerationSource(dblayer.StoreID("global/auth"), use)
	if err != nil {
		panic(err)
	}
	return source
}

type providerNamedScript struct {
	name  string
	steps []providerScriptStep
}

func acceptStagedValidation(context.Context, string) error { return nil }

type providerBackupStep struct {
	more bool
	err  error
}

type providerBackupScript struct {
	steps       []providerBackupStep
	finishErr   error
	finishCalls int
}

func (backup *providerBackupScript) Step(int32) (bool, error) {
	if len(backup.steps) == 0 {
		return false, errors.New("backup script exhausted")
	}
	step := backup.steps[0]
	backup.steps = backup.steps[1:]
	return step.more, step.err
}

func (backup *providerBackupScript) Finish() error {
	backup.finishCalls++
	return backup.finishErr
}

func providerIntegrityFailureScripts(canary error) []providerNamedScript {
	return []providerNamedScript{
		{name: "integrity query", steps: []providerScriptStep{{query: "integrity_check", err: canary}}},
		{name: "integrity result", steps: []providerScriptStep{providerRow("integrity_check", "broken")}},
		{name: "foreign query", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"), {query: "foreign_key_check", err: canary},
		}},
		{name: "foreign row", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", columns: []string{"table"}, rows: [][]driver.Value{{"child"}}},
		}},
		{name: "foreign rows error", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", columns: []string{"table"}, rowsErr: canary},
		}},
	}
}
