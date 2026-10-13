// Package errtext is a library outside the module whose function returns
// the text of the error it is handed.
package errtext

import "io"

func Describe(err error) string { return err.Error() }

// WriteTo writes the text of err to w.
func WriteTo(w io.Writer, err error) { io.WriteString(w, err.Error()) }

// Printer remembers the last error it was handed.
type Printer struct {
	last string
}

func (p *Printer) Remember(err error) { p.last = err.Error() }

func (p *Printer) Last() string { return p.last }

// DescribeAll joins the text of each error it is handed.
func DescribeAll(errs ...error) string {
	var out string
	for _, err := range errs {
		out += err.Error() + "; "
	}
	return out
}
