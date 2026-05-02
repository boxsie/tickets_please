package domain

import "errors"

// Sentinel errors returned by `svc.Service` methods. The MCP layer maps these
// onto MCP error codes; callers in pure-Go code use `errors.Is` to branch.
var (
	// ErrNotFound is returned when an entity referenced by id-or-slug does
	// not exist.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists is returned when a uniqueness constraint would be
	// violated (e.g. duplicate project slug).
	ErrAlreadyExists = errors.New("already exists")

	// ErrInvalidArgument is returned when the caller supplied a malformed
	// or out-of-range input (e.g. summary shorter than 200 chars,
	// MoveTicket targeting `done`).
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrFailedPrecondition is returned when the request is well-formed
	// but the system state forbids it (e.g. enforce_dependencies=true and
	// the ticket has unmet `depends_on`, deleting a project with active
	// tickets).
	ErrFailedPrecondition = errors.New("failed precondition")

	// ErrUnauthenticated is returned when a mutating method is called
	// without a valid agent session in the context.
	ErrUnauthenticated = errors.New("unauthenticated")
)

// IsNotFound reports whether err matches ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsAlreadyExists reports whether err matches ErrAlreadyExists.
func IsAlreadyExists(err error) bool { return errors.Is(err, ErrAlreadyExists) }

// IsInvalidArgument reports whether err matches ErrInvalidArgument.
func IsInvalidArgument(err error) bool { return errors.Is(err, ErrInvalidArgument) }

// IsFailedPrecondition reports whether err matches ErrFailedPrecondition.
func IsFailedPrecondition(err error) bool { return errors.Is(err, ErrFailedPrecondition) }

// IsUnauthenticated reports whether err matches ErrUnauthenticated.
func IsUnauthenticated(err error) bool { return errors.Is(err, ErrUnauthenticated) }
