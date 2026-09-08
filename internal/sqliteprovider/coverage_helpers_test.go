//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const providerScriptDriverName = "picoclaw-provider-coverage-script"

var (
	providerScriptRegister sync.Once
	providerScriptID       atomic.Uint64
	providerScripts        sync.Map
)

type providerScript struct {
	mu       sync.Mutex
	steps    []providerScriptStep
	closeErr error
	pingErr  error
}

type providerScriptStep struct {
	query   string
	columns []string
	rows    [][]driver.Value
	err     error
	rowsErr error
}

type providerScriptDriver struct{}

func (providerScriptDriver) Open(name string) (driver.Conn, error) {
	value, ok := providerScripts.Load(name)
	if !ok {
		return nil, errors.New("provider coverage script is missing")
	}
	return &providerScriptConn{script: value.(*providerScript)}, nil
}

type providerScriptConn struct{ script *providerScript }

func (connection *providerScriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("provider coverage prepared statements are unsupported")
}

func (connection *providerScriptConn) Begin() (driver.Tx, error) {
	return nil, errors.New("provider coverage transactions are unsupported")
}
func (connection *providerScriptConn) Close() error { return connection.script.closeErr }
func (connection *providerScriptConn) Ping(context.Context) error {
	return connection.script.pingErr
}

func (connection *providerScriptConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	step, err := connection.next(query)
	if err != nil || step.err != nil {
		if err != nil {
			return nil, err
		}
		return nil, step.err
	}
	return &providerScriptRows{
		columns: step.columns,
		rows:    step.rows,
		final:   step.rowsErr,
	}, nil
}

func (connection *providerScriptConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	step, err := connection.next(query)
	if err != nil || step.err != nil {
		if err != nil {
			return nil, err
		}
		return nil, step.err
	}
	return driver.RowsAffected(1), nil
}

func (connection *providerScriptConn) next(query string) (providerScriptStep, error) {
	connection.script.mu.Lock()
	defer connection.script.mu.Unlock()
	if len(connection.script.steps) == 0 {
		return providerScriptStep{}, errors.New("provider coverage script exhausted")
	}
	step := connection.script.steps[0]
	connection.script.steps = connection.script.steps[1:]
	if step.query != "" && !strings.Contains(query, step.query) {
		return providerScriptStep{}, errors.New("provider coverage query mismatch")
	}
	return step, nil
}

type providerScriptRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
	final   error
}

func (rows *providerScriptRows) Columns() []string { return rows.columns }
func (rows *providerScriptRows) Close() error      { return nil }
func (rows *providerScriptRows) Next(destination []driver.Value) error {
	if rows.index < len(rows.rows) {
		copy(destination, rows.rows[rows.index])
		rows.index++
		return nil
	}
	if rows.final != nil {
		err := rows.final
		rows.final = nil
		return err
	}
	return io.EOF
}

func openProviderScript(t *testing.T, steps ...providerScriptStep) *sql.DB {
	return openConfiguredProviderScript(t, nil, steps...)
}

func openConfiguredProviderScript(
	t *testing.T,
	pingErr error,
	steps ...providerScriptStep,
) *sql.DB {
	t.Helper()
	providerScriptRegister.Do(func() { sql.Register(providerScriptDriverName, providerScriptDriver{}) })
	name := "script-" + time.Now().Format("150405.000000000") + "-" +
		string(rune(providerScriptID.Add(1)))
	script := &providerScript{steps: append([]providerScriptStep(nil), steps...), pingErr: pingErr}
	providerScripts.Store(name, script)
	t.Cleanup(func() { providerScripts.Delete(name) })
	database, err := sql.Open(providerScriptDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func providerRow(query string, values ...driver.Value) providerScriptStep {
	columns := make([]string, len(values))
	for index := range columns {
		columns[index] = "value"
	}
	return providerScriptStep{query: query, columns: columns, rows: [][]driver.Value{values}}
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

type providerFaultFile struct {
	info     os.FileInfo
	statErr  error
	chmodErr error
	syncErr  error
	closeErr error
}

func (file *providerFaultFile) Stat() (os.FileInfo, error) { return file.info, file.statErr }
func (file *providerFaultFile) Chmod(os.FileMode) error    { return file.chmodErr }
func (file *providerFaultFile) Sync() error                { return file.syncErr }
func (file *providerFaultFile) Close() error               { return file.closeErr }

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

func providerIntegrityOKSteps() []providerScriptStep {
	return []providerScriptStep{
		providerRow("integrity_check", "ok"),
		{query: "foreign_key_check", columns: []string{"table"}},
	}
}
