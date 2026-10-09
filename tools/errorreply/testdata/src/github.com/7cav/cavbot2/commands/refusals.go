package commands

import "fmt"

// MissingRoleError refuses a Foxhole action naming a role the guild doesn't
// hold. The Foxhole page names the role.
type MissingRoleError struct {
	// Role is the missing role's name.
	Role string
}

func (e *MissingRoleError) Error() string {
	return fmt.Sprintf("the %q role isn't in the server", e.Role)
}

// roleNames are the Foxhole roles' names, as the commands use them.
var roleNames = map[string]string{"member": "Foxhole Member"}

// StartFoxholeAction refuses an action whose role the guild lacks.
func StartFoxholeAction(role string, held bool) error {
	if !held {
		return &MissingRoleError{Role: roleNames[role]}
	}
	return nil
}

// SweepError is a failed sweep, keeping what Discord answered.
type SweepError struct {
	Reason string
}

func (e *SweepError) Error() string { return e.Reason }

// Sweep wraps the error a sweep met.
func Sweep(err error) error {
	if err != nil {
		return &SweepError{Reason: "sweep failed: " + err.Error()}
	}
	return nil
}

// LastSweep is the last sweep's failure.
func LastSweep() error {
	return &SweepError{Reason: "sweep failed: " + lastErr.Error()}
}
