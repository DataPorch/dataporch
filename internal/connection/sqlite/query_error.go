package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/adamraziv/dataporch/internal/execution"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

type sqliteErrorPhase uint8

const (
	sqliteErrorPhaseOpen sqliteErrorPhase = iota + 1
	sqliteErrorPhasePrepare
	sqliteErrorPhaseStep
	sqliteErrorPhaseClose
)

var primaryCodeSymbols = map[sqlite3.ErrorCode]string{
	sqlite3.ERROR:      "SQLITE_ERROR",
	sqlite3.INTERNAL:   "SQLITE_INTERNAL",
	sqlite3.PERM:       "SQLITE_PERM",
	sqlite3.ABORT:      "SQLITE_ABORT",
	sqlite3.BUSY:       "SQLITE_BUSY",
	sqlite3.LOCKED:     "SQLITE_LOCKED",
	sqlite3.NOMEM:      "SQLITE_NOMEM",
	sqlite3.READONLY:   "SQLITE_READONLY",
	sqlite3.INTERRUPT:  "SQLITE_INTERRUPT",
	sqlite3.IOERR:      "SQLITE_IOERR",
	sqlite3.CORRUPT:    "SQLITE_CORRUPT",
	sqlite3.NOTFOUND:   "SQLITE_NOTFOUND",
	sqlite3.FULL:       "SQLITE_FULL",
	sqlite3.CANTOPEN:   "SQLITE_CANTOPEN",
	sqlite3.PROTOCOL:   "SQLITE_PROTOCOL",
	sqlite3.EMPTY:      "SQLITE_EMPTY",
	sqlite3.SCHEMA:     "SQLITE_SCHEMA",
	sqlite3.TOOBIG:     "SQLITE_TOOBIG",
	sqlite3.CONSTRAINT: "SQLITE_CONSTRAINT",
	sqlite3.MISMATCH:   "SQLITE_MISMATCH",
	sqlite3.MISUSE:     "SQLITE_MISUSE",
	sqlite3.NOLFS:      "SQLITE_NOLFS",
	sqlite3.AUTH:       "SQLITE_AUTH",
	sqlite3.FORMAT:     "SQLITE_FORMAT",
	sqlite3.RANGE:      "SQLITE_RANGE",
	sqlite3.NOTADB:     "SQLITE_NOTADB",
	sqlite3.NOTICE:     "SQLITE_NOTICE",
	sqlite3.WARNING:    "SQLITE_WARNING",
}

var extendedCodeSymbols = map[sqlite3.ExtendedErrorCode]string{
	sqlite3.ERROR_MISSING_COLLSEQ:   "SQLITE_ERROR_MISSING_COLLSEQ",
	sqlite3.ERROR_RETRY:             "SQLITE_ERROR_RETRY",
	sqlite3.ERROR_SNAPSHOT:          "SQLITE_ERROR_SNAPSHOT",
	sqlite3.ERROR_RESERVESIZE:       "SQLITE_ERROR_RESERVESIZE",
	sqlite3.ERROR_KEY:               "SQLITE_ERROR_KEY",
	sqlite3.ERROR_UNABLE:            "SQLITE_ERROR_UNABLE",
	sqlite3.IOERR_READ:              "SQLITE_IOERR_READ",
	sqlite3.IOERR_SHORT_READ:        "SQLITE_IOERR_SHORT_READ",
	sqlite3.IOERR_WRITE:             "SQLITE_IOERR_WRITE",
	sqlite3.IOERR_FSYNC:             "SQLITE_IOERR_FSYNC",
	sqlite3.IOERR_DIR_FSYNC:         "SQLITE_IOERR_DIR_FSYNC",
	sqlite3.IOERR_TRUNCATE:          "SQLITE_IOERR_TRUNCATE",
	sqlite3.IOERR_FSTAT:             "SQLITE_IOERR_FSTAT",
	sqlite3.IOERR_UNLOCK:            "SQLITE_IOERR_UNLOCK",
	sqlite3.IOERR_RDLOCK:            "SQLITE_IOERR_RDLOCK",
	sqlite3.IOERR_DELETE:            "SQLITE_IOERR_DELETE",
	sqlite3.IOERR_BLOCKED:           "SQLITE_IOERR_BLOCKED",
	sqlite3.IOERR_NOMEM:             "SQLITE_IOERR_NOMEM",
	sqlite3.IOERR_ACCESS:            "SQLITE_IOERR_ACCESS",
	sqlite3.IOERR_CHECKRESERVEDLOCK: "SQLITE_IOERR_CHECKRESERVEDLOCK",
	sqlite3.IOERR_LOCK:              "SQLITE_IOERR_LOCK",
	sqlite3.IOERR_CLOSE:             "SQLITE_IOERR_CLOSE",
	sqlite3.IOERR_DIR_CLOSE:         "SQLITE_IOERR_DIR_CLOSE",
	sqlite3.IOERR_SHMOPEN:           "SQLITE_IOERR_SHMOPEN",
	sqlite3.IOERR_SHMSIZE:           "SQLITE_IOERR_SHMSIZE",
	sqlite3.IOERR_SHMLOCK:           "SQLITE_IOERR_SHMLOCK",
	sqlite3.IOERR_SHMMAP:            "SQLITE_IOERR_SHMMAP",
	sqlite3.IOERR_SEEK:              "SQLITE_IOERR_SEEK",
	sqlite3.IOERR_DELETE_NOENT:      "SQLITE_IOERR_DELETE_NOENT",
	sqlite3.IOERR_MMAP:              "SQLITE_IOERR_MMAP",
	sqlite3.IOERR_GETTEMPPATH:       "SQLITE_IOERR_GETTEMPPATH",
	sqlite3.IOERR_CONVPATH:          "SQLITE_IOERR_CONVPATH",
	sqlite3.IOERR_VNODE:             "SQLITE_IOERR_VNODE",
	sqlite3.IOERR_AUTH:              "SQLITE_IOERR_AUTH",
	sqlite3.IOERR_BEGIN_ATOMIC:      "SQLITE_IOERR_BEGIN_ATOMIC",
	sqlite3.IOERR_COMMIT_ATOMIC:     "SQLITE_IOERR_COMMIT_ATOMIC",
	sqlite3.IOERR_ROLLBACK_ATOMIC:   "SQLITE_IOERR_ROLLBACK_ATOMIC",
	sqlite3.IOERR_DATA:              "SQLITE_IOERR_DATA",
	sqlite3.IOERR_CORRUPTFS:         "SQLITE_IOERR_CORRUPTFS",
	sqlite3.IOERR_IN_PAGE:           "SQLITE_IOERR_IN_PAGE",
	sqlite3.IOERR_BADKEY:            "SQLITE_IOERR_BADKEY",
	sqlite3.IOERR_CODEC:             "SQLITE_IOERR_CODEC",
	sqlite3.LOCKED_SHAREDCACHE:      "SQLITE_LOCKED_SHAREDCACHE",
	sqlite3.LOCKED_VTAB:             "SQLITE_LOCKED_VTAB",
	sqlite3.BUSY_RECOVERY:           "SQLITE_BUSY_RECOVERY",
	sqlite3.BUSY_SNAPSHOT:           "SQLITE_BUSY_SNAPSHOT",
	sqlite3.BUSY_TIMEOUT:            "SQLITE_BUSY_TIMEOUT",
	sqlite3.CANTOPEN_NOTEMPDIR:      "SQLITE_CANTOPEN_NOTEMPDIR",
	sqlite3.CANTOPEN_ISDIR:          "SQLITE_CANTOPEN_ISDIR",
	sqlite3.CANTOPEN_FULLPATH:       "SQLITE_CANTOPEN_FULLPATH",
	sqlite3.CANTOPEN_CONVPATH:       "SQLITE_CANTOPEN_CONVPATH",
	sqlite3.CANTOPEN_SYMLINK:        "SQLITE_CANTOPEN_SYMLINK",
	sqlite3.CORRUPT_VTAB:            "SQLITE_CORRUPT_VTAB",
	sqlite3.CORRUPT_SEQUENCE:        "SQLITE_CORRUPT_SEQUENCE",
	sqlite3.CORRUPT_INDEX:           "SQLITE_CORRUPT_INDEX",
	sqlite3.READONLY_RECOVERY:       "SQLITE_READONLY_RECOVERY",
	sqlite3.READONLY_CANTLOCK:       "SQLITE_READONLY_CANTLOCK",
	sqlite3.READONLY_ROLLBACK:       "SQLITE_READONLY_ROLLBACK",
	sqlite3.READONLY_DBMOVED:        "SQLITE_READONLY_DBMOVED",
	sqlite3.READONLY_CANTINIT:       "SQLITE_READONLY_CANTINIT",
	sqlite3.READONLY_DIRECTORY:      "SQLITE_READONLY_DIRECTORY",
	sqlite3.ABORT_ROLLBACK:          "SQLITE_ABORT_ROLLBACK",
	sqlite3.CONSTRAINT_CHECK:        "SQLITE_CONSTRAINT_CHECK",
	sqlite3.CONSTRAINT_COMMITHOOK:   "SQLITE_CONSTRAINT_COMMITHOOK",
	sqlite3.CONSTRAINT_FOREIGNKEY:   "SQLITE_CONSTRAINT_FOREIGNKEY",
	sqlite3.CONSTRAINT_FUNCTION:     "SQLITE_CONSTRAINT_FUNCTION",
	sqlite3.CONSTRAINT_NOTNULL:      "SQLITE_CONSTRAINT_NOTNULL",
	sqlite3.CONSTRAINT_PRIMARYKEY:   "SQLITE_CONSTRAINT_PRIMARYKEY",
	sqlite3.CONSTRAINT_TRIGGER:      "SQLITE_CONSTRAINT_TRIGGER",
	sqlite3.CONSTRAINT_UNIQUE:       "SQLITE_CONSTRAINT_UNIQUE",
	sqlite3.CONSTRAINT_VTAB:         "SQLITE_CONSTRAINT_VTAB",
	sqlite3.CONSTRAINT_ROWID:        "SQLITE_CONSTRAINT_ROWID",
	sqlite3.CONSTRAINT_PINNED:       "SQLITE_CONSTRAINT_PINNED",
	sqlite3.CONSTRAINT_DATATYPE:     "SQLITE_CONSTRAINT_DATATYPE",
	sqlite3.NOTICE_RECOVER_WAL:      "SQLITE_NOTICE_RECOVER_WAL",
	sqlite3.NOTICE_RECOVER_ROLLBACK: "SQLITE_NOTICE_RECOVER_ROLLBACK",
	sqlite3.NOTICE_RBU:              "SQLITE_NOTICE_RBU",
	sqlite3.WARNING_AUTOINDEX:       "SQLITE_WARNING_AUTOINDEX",
	sqlite3.AUTH_USER:               "SQLITE_AUTH_USER",
}

func primaryCodeSymbol(code sqlite3.ErrorCode) string {
	if symbol, ok := primaryCodeSymbols[code]; ok {
		return symbol
	}
	return "SQLITE_UNKNOWN"
}

func extendedCodeSymbol(code sqlite3.ExtendedErrorCode) string {
	if symbol, ok := extendedCodeSymbols[code]; ok {
		return symbol
	}
	if symbol := primaryCodeSymbol(code.Code()); code == sqlite3.ExtendedErrorCode(code.Code()) {
		return symbol
	}
	return "SQLITE_UNKNOWN"
}

type extractedSQLiteError struct {
	code     sqlite3.ErrorCode
	extended sqlite3.ExtendedErrorCode
	message  string
}

func extractSQLiteError(err error) (extractedSQLiteError, bool) {
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr != nil {
		return extractedSQLiteError{
			code:     sqliteErr.Code(),
			extended: sqliteErr.ExtendedCode(),
			message:  sqliteErr.Error(),
		}, true
	}
	var extended sqlite3.ExtendedErrorCode
	if errors.As(err, &extended) {
		return extractedSQLiteError{
			code:     extended.Code(),
			extended: extended,
			message:  extended.Error(),
		}, true
	}
	var code sqlite3.ErrorCode
	if errors.As(err, &code) {
		return extractedSQLiteError{
			code:     code,
			extended: code.ExtendedCode(),
			message:  code.Error(),
		}, true
	}
	return extractedSQLiteError{}, false
}

func projectSQLiteError(
	requestContext context.Context,
	queryContext context.Context,
	err error,
	phase sqliteErrorPhase,
) error {
	if err == nil {
		return nil
	}
	if requestContext != nil {
		switch requestContext.Err() {
		case context.Canceled:
			return fmt.Errorf("%w: %w", execution.ErrCancelled, context.Canceled)
		case context.DeadlineExceeded:
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, context.DeadlineExceeded)
		}
	}
	if queryContext != nil {
		switch queryContext.Err() {
		case context.DeadlineExceeded:
			return fmt.Errorf("%w: %w", execution.ErrQueryTimeout, err)
		case context.Canceled:
			return fmt.Errorf("%w: %w", execution.ErrQueryCancelled, err)
		}
	}
	if isProjectedSQLiteError(err) || isKnownQueryError(err) {
		return err
	}
	if errors.Is(err, errSQLiteFileUnavailable) || errors.Is(err, errRuntimeUnavailable) || errors.Is(err, errRuntimeClosed) {
		return execution.ErrDatabaseUnavailable
	}

	extracted, ok := extractSQLiteError(err)
	if !ok {
		return execution.NewDatabaseFailure(
			execution.ErrorCategoryDatabaseError,
			false,
			&execution.DatabaseError{
				Kind:    Kind,
				Code:    "SQLITE_UNKNOWN",
				Message: "SQLite database operation failed.",
			},
		)
	}

	code := extracted.code
	extendedSymbol := extendedCodeSymbol(extracted.extended)
	primarySymbol := primaryCodeSymbol(code)
	message := sqliteDiagnostic(extracted.message)
	if phase == sqliteErrorPhaseOpen {
		message = "SQLite database setup failed."
	}
	databaseError := &execution.DatabaseError{
		Kind:    Kind,
		Code:    primarySymbol,
		Message: message,
	}
	if extendedSymbol != primarySymbol && extendedSymbol != "SQLITE_UNKNOWN" {
		databaseError.ExtendedCode = extendedSymbol
	}

	switch {
	case code == sqlite3.AUTH || code == sqlite3.PERM:
		return execution.NewDatabaseFailure(execution.ErrorCategoryDatabasePermissionDenied, false, databaseError)
	case code == sqlite3.READONLY:
		return execution.NewDatabaseFailure(execution.ErrorCategoryReadOnlyViolation, false, databaseError)
	case code == sqlite3.BUSY || code == sqlite3.LOCKED:
		return execution.NewDatabaseFailure(execution.ErrorCategoryDatabaseConflict, true, databaseError)
	case code == sqlite3.INTERRUPT:
		return execution.NewDatabaseFailure(execution.ErrorCategoryQueryCancelled, false, databaseError)
	case code == sqlite3.CANTOPEN || code == sqlite3.IOERR:
		return execution.NewDatabaseFailure(execution.ErrorCategoryDatabaseUnavailable, true, databaseError)
	case (code == sqlite3.ERROR || code == sqlite3.RANGE || code == sqlite3.MISMATCH || code == sqlite3.MISUSE) && phase == sqliteErrorPhasePrepare:
		return execution.NewDatabaseFailure(execution.ErrorCategoryInvalidQuery, false, databaseError)
	case code == sqlite3.TOOBIG || code == sqlite3.NOMEM || code == sqlite3.FULL:
		return execution.NewDatabaseFailure(execution.ErrorCategoryDatabaseResourceExhausted, false, databaseError)
	default:
		return execution.NewDatabaseFailure(execution.ErrorCategoryDatabaseError, false, databaseError)
	}
}

func isProjectedSQLiteError(err error) bool {
	var failure *execution.DatabaseFailure
	return errors.As(err, &failure) && failure != nil
}

func isKnownQueryError(err error) bool {
	for _, sentinel := range []error{
		execution.ErrInvalidRequest,
		execution.ErrInvalidQuery,
		execution.ErrReadOnlyViolation,
		execution.ErrQueryCancelled,
		execution.ErrResultTooLarge,
		execution.ErrCancelled,
		execution.ErrQueryTimeout,
		execution.ErrDatabasePermissionDenied,
		execution.ErrDatabaseConflict,
		execution.ErrDatabaseUnavailable,
		execution.ErrDatabaseResourceExhausted,
		execution.ErrDatabaseError,
		execution.ErrInternal,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

func sqliteDiagnostic(message string) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	message = strings.ReplaceAll(message, "\x00", "")
	fields := strings.Fields(message)
	for index, field := range fields {
		if strings.Contains(field, "/") || strings.HasPrefix(field, "file:") {
			fields[index] = "[redacted]"
		}
	}
	message = strings.Join(fields, " ")
	if message == "" {
		return "SQLite database error."
	}
	if len(message) <= 512 {
		return message
	}
	message = message[:512]
	for len(message) > 0 && !utf8.RuneStart(message[len(message)-1]) {
		message = message[:len(message)-1]
	}
	return message
}
