package runtimehost

// Preserve the sentinel outcome and originating cause without printing the
// cause. Callers should use errors.Is/As to inspect these errors.
type operationFailure struct {
	outcome, cause error
}

func (e *operationFailure) Error() string   { return e.outcome.Error() }
func (e *operationFailure) Unwrap() []error { return []error{e.outcome, e.cause} }

func retainCause(outcome, cause error) error {
	if cause == nil {
		return outcome
	}
	return &operationFailure{outcome: outcome, cause: cause}
}
