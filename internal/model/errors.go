package model

import "errors"

type ExitCode int

const (
	ExitOK ExitCode = iota
	ExitGeneral
	ExitDrift
	ExitLinkIntegrity
)

type ExitError struct {
	Code  ExitCode
	Field string
	Err   error
}

func (e *ExitError) Error() string {
	msg := ""
	if e.Err != nil {
		msg = e.Err.Error()
	}
	if e.Field != "" {
		if msg == "" {
			return e.Field
		}
		return e.Field + ": " + msg
	}
	return msg
}

func (e *ExitError) Unwrap() error { return e.Err }

func ExitCodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var failure *ExitError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ExitGeneral
}

func invalid(field string, err error) error {
	return &ExitError{Code: ExitGeneral, Field: field, Err: err}
}
