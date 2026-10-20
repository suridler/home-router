package fault

import "fmt"

type Code string

const (
	CodeNotFound         Code = "not_found"
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeRequestTooLarge  Code = "request_too_large"
)

type Error struct {
	Code       Code
	Message    string
	HTTPStatus int
	Cause      error
}

func (err *Error) Error() string {
	if err.Cause != nil {
		return fmt.Sprintf("%s: %v", err.Message, err.Cause)
	}
	return err.Message
}

func (err *Error) Unwrap() error { return err.Cause }
