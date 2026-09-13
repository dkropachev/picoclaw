package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/databasevalidation"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

const (
	maximumInspectionValidationStatementBytes = 64 << 10
	maximumInspectionValidationArguments      = 1024
	maximumInspectionValidationArgumentBytes  = 64 << 10
	maximumInspectionValidationScalarBytes    = 1 << 20
	inspectionValidationCleanupTimeout        = time.Second
	maximumInspectionValidationDuration       = 30 * time.Second
)

var (
	errInspectionValidationContract = errors.New(
		"SQLite inspection validation contract was violated",
	)
	errInspectionValidationQueryUnavailable = errors.New(
		"SQLite inspection validation query is unavailable",
	)
	errInspectionValidationScopeExpired = errors.New(
		"SQLite inspection validation scope has expired",
	)
	errInspectionValidationDeadline = errors.Join(
		context.DeadlineExceeded,
		errors.New("SQLite inspection validation exceeded its maximum duration"),
	)
	errInspectionValidationQueryOnlyState = errors.New(
		"SQLite inspection validation query-only state is invalid",
	)
)

type inspectionValidationState struct {
	sync.Mutex
	wait       sync.WaitGroup
	active     bool
	inFlight   bool
	storeID    dblayer.StoreID
	domain     string
	scope      context.Context
	connection *sql.Conn
	readScalar func(
		context.Context,
		*sql.Conn,
		string,
		[]string,
	) (databasevalidation.Scalar, error)
	beforeReadAdmission func()
	failure             error
}

type inspectionValidationGeneration struct {
	state *inspectionValidationState
}

type inspectionValidationBoundary interface {
	BeginAttempted() error
	Started() error
	Check(ctx context.Context) error
	Close() error
}

type inspectionValidationOps struct {
	validateQuery    func(context.Context, Inspection) error
	revalidate       func(context.Context, Inspection) error
	connection       func(context.Context, Inspection) (*sql.Conn, error)
	requireQueryOnly func(context.Context, *sql.Conn, bool) error
	setQueryOnly     func(context.Context, *sql.Conn, bool) error
	newBoundary      func(*sql.Conn) (inspectionValidationBoundary, error)
	begin            func(context.Context, *sql.Conn) error
	readScalar       func(
		context.Context,
		*sql.Conn,
		string,
		[]string,
	) (databasevalidation.Scalar, error)
	beforeReadAdmission func()
	discardConnection   func(*sql.Conn) error
	closeConnection     func(*sql.Conn) error
	release             func(Inspection) error
}

type inspectionValidationLimits struct {
	maximumDuration time.Duration
	cleanupTimeout  time.Duration
	ops             inspectionValidationOps
}

func defaultInspectionValidationOps() inspectionValidationOps {
	return inspectionValidationOps{
		validateQuery: func(ctx context.Context, inspection Inspection) error {
			return inspection.validateQuery(ctx)
		},
		revalidate: func(ctx context.Context, inspection Inspection) error {
			return inspection.Revalidate(ctx)
		},
		connection: func(ctx context.Context, inspection Inspection) (*sql.Conn, error) {
			return inspection.database.Conn(ctx)
		},
		requireQueryOnly: requireInspectionValidationQueryOnly,
		setQueryOnly:     setInspectionValidationQueryOnly,
		newBoundary: func(connection *sql.Conn) (inspectionValidationBoundary, error) {
			return NewTransactionBoundary(connection)
		},
		begin: func(ctx context.Context, connection *sql.Conn) error {
			_, err := connection.ExecContext(ctx, "BEGIN")
			return err
		},
		readScalar:          readInspectionValidationScalar,
		beforeReadAdmission: func() {},
		discardConnection:   discardInspectionValidationConnection,
		closeConnection: func(connection *sql.Conn) error {
			if connection == nil {
				return nil
			}
			return connection.Close()
		},
		release: releaseInspectionValidationOwner,
	}
}

func completeInspectionValidationOps(ops inspectionValidationOps) inspectionValidationOps {
	defaults := defaultInspectionValidationOps()
	if ops.validateQuery == nil {
		ops.validateQuery = defaults.validateQuery
	}
	if ops.revalidate == nil {
		ops.revalidate = defaults.revalidate
	}
	if ops.connection == nil {
		ops.connection = defaults.connection
	}
	if ops.requireQueryOnly == nil {
		ops.requireQueryOnly = defaults.requireQueryOnly
	}
	if ops.setQueryOnly == nil {
		ops.setQueryOnly = defaults.setQueryOnly
	}
	if ops.newBoundary == nil {
		ops.newBoundary = defaults.newBoundary
	}
	if ops.begin == nil {
		ops.begin = defaults.begin
	}
	if ops.readScalar == nil {
		ops.readScalar = defaults.readScalar
	}
	if ops.beforeReadAdmission == nil {
		ops.beforeReadAdmission = defaults.beforeReadAdmission
	}
	if ops.discardConnection == nil {
		ops.discardConnection = defaults.discardConnection
	}
	if ops.closeConnection == nil {
		ops.closeConnection = defaults.closeConnection
	}
	if ops.release == nil {
		ops.release = defaults.release
	}
	return ops
}

func (generation inspectionValidationGeneration) StoreID() dblayer.StoreID {
	if generation.state == nil {
		return ""
	}
	generation.state.Lock()
	defer generation.state.Unlock()
	if !generation.state.active {
		return ""
	}
	return generation.state.storeID
}

func (generation inspectionValidationGeneration) Domain() string {
	if generation.state == nil {
		return ""
	}
	generation.state.Lock()
	defer generation.state.Unlock()
	if !generation.state.active {
		return ""
	}
	return generation.state.domain
}

func (generation inspectionValidationGeneration) ReadScalar(
	statement string,
	arguments ...string,
) (databasevalidation.Scalar, error) {
	state := generation.state
	if state == nil {
		return databasevalidation.Scalar{}, inspectionValidationContractFailure(
			"SQLite inspection validation generation is unavailable",
		)
	}
	state.Lock()
	active := state.active
	beforeReadAdmission := state.beforeReadAdmission
	state.Unlock()
	if !active {
		return databasevalidation.Scalar{}, errInspectionValidationScopeExpired
	}
	tooManyArguments := len(arguments) > maximumInspectionValidationArguments
	if !tooManyArguments {
		arguments = append([]string(nil), arguments...)
	}
	var contractErr error
	if tooManyArguments {
		contractErr = inspectionValidationContractFailure(
			"SQLite inspection validation scalar arguments exceed their limit",
		)
	} else if !validInspectionValidationSelect(statement) ||
		!validInspectionValidationArguments(arguments) {
		contractErr = inspectionValidationContractFailure(
			"SQLite inspection validation scalar read is invalid",
		)
	}
	if beforeReadAdmission != nil {
		beforeReadAdmission()
	}
	state.Lock()
	if !state.active {
		state.Unlock()
		return databasevalidation.Scalar{}, errInspectionValidationScopeExpired
	}
	if contractErr != nil {
		state.failure = errors.Join(state.failure, contractErr)
		state.Unlock()
		return databasevalidation.Scalar{}, contractErr
	}
	if state.inFlight || state.connection == nil || state.scope == nil || state.readScalar == nil {
		err := inspectionValidationContractFailure(
			"SQLite inspection validation scalar read is concurrent or unavailable",
		)
		state.failure = errors.Join(state.failure, err)
		state.Unlock()
		return databasevalidation.Scalar{}, err
	}
	state.inFlight = true
	state.wait.Add(1)
	scope := state.scope
	connection := state.connection
	readScalar := state.readScalar
	state.Unlock()
	defer func() {
		state.Lock()
		state.inFlight = false
		state.Unlock()
		state.wait.Done()
	}()
	if cause := context.Cause(scope); cause != nil {
		state.recordFailure(cause)
		return databasevalidation.Scalar{}, cause
	}
	result, err := readScalar(
		scope,
		connection,
		statement,
		arguments,
	)
	if err != nil {
		classifiedErr := err
		if !errors.Is(classifiedErr, errInspectionInfrastructure) &&
			!errors.Is(classifiedErr, errInspectionIntegrity) {
			err = inspectionValidationQueryFailure(err)
			classifiedErr = err
		}
		err = inspectionValidationCausePrecedence(scope, classifiedErr)
		state.recordFailure(err)
		return databasevalidation.Scalar{}, err
	}
	if cause := context.Cause(scope); cause != nil {
		state.recordFailure(cause)
		return databasevalidation.Scalar{}, cause
	}
	return result, nil
}

func inspectionValidationQueryFailure(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(
		errInspectionValidationQueryUnavailable,
		classifyInspectionDatabaseError(err, IsBusyOrLocked),
		err,
	)
}

func inspectionValidationCausePrecedence(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(err, errInspectionInfrastructure) ||
			errors.Is(err, errInspectionIntegrity) {
			return errors.Join(cause, err)
		}
		return cause
	}
	return err
}

func (state *inspectionValidationState) recordFailure(err error) {
	if state == nil || err == nil {
		return
	}
	state.Lock()
	state.failure = errors.Join(state.failure, err)
	state.Unlock()
}

func inspectionValidationContractFailure(message string) error {
	return errors.Join(
		errInspectionInfrastructure,
		errInspectionValidationContract,
		errors.New(message),
	)
}

func validInspectionValidationArguments(arguments []string) bool {
	if len(arguments) > maximumInspectionValidationArguments {
		return false
	}
	total := 0
	for _, argument := range arguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) ||
			len(argument) > maximumInspectionValidationArgumentBytes-total {
			return false
		}
		total += len(argument)
	}
	return true
}

func readInspectionValidationScalar(
	ctx context.Context,
	connection *sql.Conn,
	statement string,
	arguments []string,
) (result databasevalidation.Scalar, resultErr error) {
	if ctx == nil || connection == nil {
		return result, inspectionValidationContractFailure(
			"SQLite inspection validation scalar reader is unavailable",
		)
	}
	queryArguments := make([]any, len(arguments))
	for index := range arguments {
		queryArguments[index] = arguments[index]
	}
	rows, err := connection.QueryContext(ctx, statement, queryArguments...)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				inspectionValidationContractFailure(
					"SQLite inspection validation rows could not close",
				),
				closeErr,
			)
		}
	}()
	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	if len(columns) != 1 {
		return result, inspectionValidationContractFailure(
			"SQLite inspection validation query returned multiple columns",
		)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return result, err
		}
		return databasevalidation.Scalar{Kind: databasevalidation.ScalarNoRow}, nil
	}
	var value any
	if err := rows.Scan(&value); err != nil {
		return result, err
	}
	if rows.Next() {
		return result, inspectionValidationContractFailure(
			"SQLite inspection validation query returned multiple rows",
		)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	switch value := value.(type) {
	case nil:
		return databasevalidation.Scalar{Kind: databasevalidation.ScalarNull}, nil
	case int64:
		return databasevalidation.Scalar{
			Kind: databasevalidation.ScalarInt64, Int64: value,
		}, nil
	case string:
		if len(value) > maximumInspectionValidationScalarBytes || !utf8.ValidString(value) ||
			strings.ContainsRune(value, 0) {
			return result, inspectionValidationContractFailure(
				"SQLite inspection validation text result is invalid",
			)
		}
		return databasevalidation.Scalar{
			Kind: databasevalidation.ScalarText, Text: strings.Clone(value),
		}, nil
	default:
		return result, inspectionValidationContractFailure(
			"SQLite inspection validation scalar type is unsupported",
		)
	}
}

// ValidateDomain invokes one exact domain validator against the retained pool
// through a callback-scoped scalar SELECT facade. The provider enables and
// verifies SQLite query-only mode, owns a guarded transaction that always
// rolls back, restores connection state, and physically revalidates the exact
// generation before and after use.
func (inspection Inspection) ValidateDomain(
	ctx context.Context,
	storeID dblayer.StoreID,
	domain string,
	validate func(context.Context, databasevalidation.Generation) error,
) error {
	return inspection.validateDomainWithLimits(
		ctx,
		storeID,
		domain,
		validate,
		inspectionValidationLimits{
			maximumDuration: maximumInspectionValidationDuration,
			cleanupTimeout:  inspectionValidationCleanupTimeout,
			ops:             defaultInspectionValidationOps(),
		},
	)
}

func (inspection Inspection) validateDomainWithLimits(
	ctx context.Context,
	storeID dblayer.StoreID,
	domain string,
	validate func(context.Context, databasevalidation.Generation) error,
	limits inspectionValidationLimits,
) (resultErr error) {
	if ctx == nil || !storeID.Valid() || !validInspectionValidationDomain(domain) ||
		validate == nil || !inspection.Exists || inspection.Empty ||
		limits.maximumDuration <= 0 || limits.cleanupTimeout <= 0 {
		return errors.Join(
			errInspectionUnavailable,
			errors.New("SQLite inspection domain validation is unavailable"),
		)
	}
	if inspection.lifecycle == nil || !inspection.lifecycle.CompareAndSwap(
		inspectionLifecycleIdle,
		inspectionLifecycleValidating,
	) {
		return inspectionValidationContractFailure(
			"SQLite inspection domain-validation lifecycle is unavailable",
		)
	}
	defer func() {
		target := inspectionLifecycleIdle
		if resultErr != nil {
			target = inspectionLifecycleInvalid
		}
		inspection.lifecycle.CompareAndSwap(
			inspectionLifecycleValidating,
			target,
		)
	}()
	ops := completeInspectionValidationOps(limits.ops)
	if err := ops.validateQuery(ctx, inspection); err != nil {
		return abortInspectionDomainValidation(
			inspection,
			nil,
			nil,
			err,
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := ops.revalidate(ctx, inspection); err != nil {
		return abortInspectionDomainValidation(
			inspection,
			nil,
			nil,
			err,
			limits.cleanupTimeout,
			ops,
		)
	}
	connection, connectionErr := ops.connection(ctx, inspection)
	if connectionErr != nil {
		return abortInspectionDomainValidation(
			inspection,
			nil,
			nil,
			errors.Join(
				errInspectionUnavailable,
				errors.New("SQLite inspection validation connection is unavailable"),
				connectionErr,
			),
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := ops.requireQueryOnly(ctx, connection, false); err != nil {
		failure := inspectionValidationQueryFailure(err)
		if errors.Is(err, errInspectionValidationQueryOnlyState) {
			failure = errors.Join(errInspectionInfrastructure, err)
		}
		failure = inspectionValidationCausePrecedence(ctx, failure)
		return abortInspectionDomainValidation(
			inspection,
			connection,
			nil,
			failure,
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := ops.setQueryOnly(ctx, connection, true); err != nil {
		failure := inspectionValidationQueryFailure(err)
		if errors.Is(err, errInspectionValidationQueryOnlyState) {
			failure = errors.Join(errInspectionInfrastructure, err)
		}
		failure = inspectionValidationCausePrecedence(ctx, failure)
		return abortInspectionDomainValidation(
			inspection,
			connection,
			nil,
			failure,
			limits.cleanupTimeout,
			ops,
		)
	}
	boundary, err := ops.newBoundary(connection)
	if err != nil {
		return abortInspectionDomainValidation(
			inspection,
			connection,
			nil,
			errors.Join(errInspectionInfrastructure, err),
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := boundary.BeginAttempted(); err != nil {
		return abortInspectionDomainValidation(
			inspection,
			connection,
			boundary,
			errors.Join(errInspectionInfrastructure, err),
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := ops.begin(ctx, connection); err != nil {
		return abortInspectionDomainValidation(
			inspection,
			connection,
			boundary,
			inspectionValidationCausePrecedence(
				ctx,
				inspectionValidationQueryFailure(err),
			),
			limits.cleanupTimeout,
			ops,
		)
	}
	if err := boundary.Started(); err != nil {
		return abortInspectionDomainValidation(
			inspection,
			connection,
			boundary,
			errors.Join(errInspectionInfrastructure, err),
			limits.cleanupTimeout,
			ops,
		)
	}

	validationCtx, cancelValidation := context.WithTimeoutCause(
		ctx,
		limits.maximumDuration,
		errInspectionValidationDeadline,
	)
	defer cancelValidation()
	scope, cancelScope := context.WithCancelCause(validationCtx)
	state := &inspectionValidationState{
		active: true, storeID: storeID, domain: domain,
		scope: scope, connection: connection,
		readScalar:          ops.readScalar,
		beforeReadAdmission: ops.beforeReadAdmission,
	}
	callbackDone := make(chan error, 1)
	go func() {
		var callbackErr error
		completed := false
		defer func() {
			recovered := recover()
			switch {
			case recovered != nil:
				callbackErr = errors.Join(
					errInspectionInfrastructure,
					errors.New("SQLite inspection domain validator panicked"),
				)
			case !completed:
				callbackErr = inspectionValidationContractFailure(
					"SQLite inspection domain validator terminated without returning",
				)
			}
			if cause := context.Cause(validationCtx); cause != nil &&
				!errors.Is(callbackErr, cause) {
				callbackErr = errors.Join(callbackErr, cause)
			}
			callbackDone <- callbackErr
		}()
		callbackErr = validate(
			scope,
			inspectionValidationGeneration{state: state},
		)
		completed = true
	}()
	var callbackErr error
	select {
	case callbackErr = <-callbackDone:
	case <-scope.Done():
		deadlineErr := context.Cause(scope)
		state.Lock()
		state.active = false
		state.Unlock()
		cancelScope(deadlineErr)
		callbackErr = errors.Join(deadlineErr, <-callbackDone)
	}
	state.Lock()
	state.active = false
	returnedDuringQuery := state.inFlight
	state.Unlock()
	cancelScope(errInspectionValidationScopeExpired)
	state.wait.Wait()
	state.Lock()
	readErr := state.failure
	state.storeID = ""
	state.domain = ""
	state.scope = nil
	state.connection = nil
	state.readScalar = nil
	state.beforeReadAdmission = nil
	state.failure = nil
	state.Unlock()
	if readErr != nil && !errors.Is(callbackErr, readErr) {
		callbackErr = errors.Join(callbackErr, readErr)
	}
	if returnedDuringQuery {
		callbackErr = errors.Join(
			callbackErr,
			inspectionValidationContractFailure(
				"SQLite inspection validator returned during a scalar read",
			),
		)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(
		context.Background(),
		limits.cleanupTimeout,
	)
	queryOnlyErr := ops.requireQueryOnly(
		cleanupCtx,
		connection,
		true,
	)
	boundaryErr := boundary.Check(cleanupCtx)
	cancelCleanup()
	if queryOnlyErr != nil || boundaryErr != nil {
		callbackErr = errors.Join(
			callbackErr,
			errInspectionInfrastructure,
			queryOnlyErr,
			boundaryErr,
		)
	}
	resultErr = classifyInspectionDomainValidationError(ctx, callbackErr)
	finishErr := finishInspectionDomainValidation(
		inspection, connection, boundary, resultErr, limits.cleanupTimeout, ops,
	)
	if errors.Is(finishErr, errInspectionInfrastructure) {
		return finishErr
	}
	return errors.Join(finishErr, ops.revalidate(ctx, inspection))
}

func classifyInspectionDomainValidationError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		if errors.Is(err, errInspectionInfrastructure) ||
			errors.Is(err, errInspectionIntegrity) {
			return errors.Join(cause, err)
		}
		return cause
	}
	if errors.Is(err, errInspectionValidationDeadline) {
		return err
	}
	if err == nil || errors.Is(err, errInspectionInfrastructure) ||
		errors.Is(err, errInspectionValidationQueryUnavailable) {
		return err
	}
	return errors.Join(
		errInspectionIntegrity,
		errors.New("SQLite inspection exact domain validation failed"),
		err,
	)
}

func abortInspectionDomainValidation(
	inspection Inspection,
	connection *sql.Conn,
	boundary inspectionValidationBoundary,
	prior error,
	cleanupTimeout time.Duration,
	ops inspectionValidationOps,
) error {
	resultErr := finishInspectionDomainValidation(
		inspection,
		connection,
		boundary,
		prior,
		cleanupTimeout,
		ops,
	)
	if errors.Is(resultErr, errInspectionInfrastructure) {
		return resultErr
	}
	releaseErr := ops.release(inspection)
	if releaseErr == nil {
		return resultErr
	}
	return errors.Join(
		errInspectionInfrastructure,
		errors.New("SQLite inspection domain-validation owner release failed"),
		resultErr,
		releaseErr,
	)
}

func finishInspectionDomainValidation(
	inspection Inspection,
	connection *sql.Conn,
	boundary inspectionValidationBoundary,
	prior error,
	cleanupTimeout time.Duration,
	ops inspectionValidationOps,
) error {
	if connection == nil {
		return prior
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(
		context.Background(),
		cleanupTimeout,
	)
	defer cancelCleanup()
	var boundaryErr error
	if boundary != nil {
		boundaryErr = boundary.Close()
	}
	resetErr := ops.setQueryOnly(cleanupCtx, connection, false)
	var discardErr error
	if errors.Is(prior, errInspectionInfrastructure) || boundaryErr != nil || resetErr != nil {
		discardErr = ops.discardConnection(connection)
	}
	closeErr := ops.closeConnection(connection)
	cleanupErr := errors.Join(boundaryErr, resetErr, discardErr, closeErr)
	if cleanupErr == nil && !errors.Is(prior, errInspectionInfrastructure) {
		return prior
	}
	return errors.Join(
		errInspectionInfrastructure,
		errors.New("SQLite inspection domain-validation cleanup failed"),
		prior,
		cleanupErr,
		ops.release(inspection),
	)
}

func discardInspectionValidationConnection(connection *sql.Conn) error {
	if connection == nil {
		return nil
	}
	err := connection.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}

func releaseInspectionValidationOwner(inspection Inspection) error {
	if inspection.database == nil || inspection.key == "" || inspection.released == nil ||
		inspection.lifecycle == nil {
		return errors.New("SQLite inspection validation owner is unavailable")
	}
	if !inspection.lifecycle.CompareAndSwap(
		inspectionLifecycleValidating,
		inspectionLifecycleTerminal,
	) {
		if inspection.lifecycle.Load() == inspectionLifecycleTerminal {
			return nil
		}
		return errors.New("SQLite inspection validation owner lifecycle changed")
	}
	if !inspection.released.CompareAndSwap(false, true) {
		return nil
	}
	return releaseInspectedPool(
		inspection.key,
		inspection.database,
		inspection.released,
	)
}

func setInspectionValidationQueryOnly(
	ctx context.Context,
	connection *sql.Conn,
	enabled bool,
) error {
	if ctx == nil || connection == nil {
		return errors.New("SQLite inspection query-only transition is unavailable")
	}
	value := "OFF"
	if enabled {
		value = "ON"
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = "+value); err != nil {
		return err
	}
	return requireInspectionValidationQueryOnly(ctx, connection, enabled)
}

func requireInspectionValidationQueryOnly(
	ctx context.Context,
	connection *sql.Conn,
	enabled bool,
) error {
	if ctx == nil || connection == nil {
		return errors.New("SQLite inspection query-only check is unavailable")
	}
	want := 0
	if enabled {
		want = 1
	}
	var current int
	if err := connection.QueryRowContext(ctx, "PRAGMA query_only").Scan(&current); err != nil {
		return err
	}
	if current != want {
		return errInspectionValidationQueryOnlyState
	}
	return nil
}

func validInspectionValidationSelect(statement string) bool {
	if statement == "" || len(statement) > maximumInspectionValidationStatementBytes ||
		!utf8.ValidString(statement) || strings.ContainsRune(statement, 0) ||
		strings.ContainsRune(statement, ';') {
		return false
	}
	trimmed := strings.TrimSpace(statement)
	if len(trimmed) <= len("select") ||
		!strings.EqualFold(trimmed[:len("select")], "select") ||
		!inspectionValidationWhitespace(trimmed[len("select")]) {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, forbidden := range []string{
		"pragma_database_list",
		"pragma_module_list",
		"sqlite_dbpage",
		"sqlite_dbdata",
		"sqlite_dbptr",
		"load_extension",
		"readfile",
		"writefile",
	} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	withoutAllowedPragma := strings.ReplaceAll(lower, "pragma_index_list", "")
	return !strings.Contains(withoutAllowedPragma, "pragma_")
}

func inspectionValidationWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

func validInspectionValidationDomain(domain string) bool {
	if domain == "" || len(domain) > 64 || domain != strings.TrimSpace(domain) ||
		!utf8.ValidString(domain) {
		return false
	}
	for index := 0; index < len(domain); index++ {
		character := domain[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if character != '-' || index == 0 || index == len(domain)-1 {
			return false
		}
	}
	return true
}

var _ databasevalidation.Generation = inspectionValidationGeneration{}
