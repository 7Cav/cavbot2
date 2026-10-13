package errordata

import (
	"fmt"
	"io"
	"strings"

	"github.com/7cav/cavbot2/utils"
)

// #645: a helper writes an error's text into a container after turning it
// into another interface.

type stringWriter interface {
	io.Writer
	WriteString(string) (int, error)
}

func describe(w stringWriter, err error) { fmt.Fprint(w, err) } // want ""

func writeAsserted(w io.Writer, err error) { w.(io.StringWriter).WriteString(err.Error()) } // want ""

func Converted(err error) {
	var b strings.Builder
	describe(&b, err)
	utils.HandleError(nil, nil, b.String())
}

func Asserted(err error) {
	var b strings.Builder
	writeAsserted(&b, err)
	utils.HandleError(nil, nil, b.String())
}

// #646: a package-level builder written through an interface.

var lastFailure strings.Builder

func Record(err error) { fmt.Fprintf(&lastFailure, "%v", err) } // want ""

func ReplyWithLast() { utils.HandleError(nil, nil, lastFailure.String()) }
