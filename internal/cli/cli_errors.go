package cli

import (
	"errors"
	"fmt"
)

const (
	exitSuccess = 0
	exitFailure = 1
	exitUsage   = 2
	exitStopped = 3
)

type cliError struct {
	code    int
	message string
	cause   error
	silent  bool
}

func (e *cliError) Error() string { return e.message }
func (e *cliError) Unwrap() error { return e.cause }

func usageError(message string, cause error) error {
	return &cliError{code: exitUsage, message: message, cause: cause}
}

func stoppedResult() error { return &cliError{code: exitStopped, silent: true} }

func exitCode(err error) int {
	var commandErr *cliError
	if errors.As(err, &commandErr) {
		return commandErr.code
	}
	return exitFailure
}

func writeDiagnostic(writer interface{ Write([]byte) (int, error) }, message string) error {
	if writer == nil {
		return errors.New("standard error is required")
	}
	if _, err := fmt.Fprintf(writer, "dataporch: %s\n", message); err != nil {
		return fmt.Errorf("writing diagnostic: %w", err)
	}
	return nil
}
