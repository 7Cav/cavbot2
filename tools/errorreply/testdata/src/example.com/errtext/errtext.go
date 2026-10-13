// Package errtext is a library outside the module whose function returns
// the text of the error it is handed.
package errtext

func Describe(err error) string { return err.Error() }
