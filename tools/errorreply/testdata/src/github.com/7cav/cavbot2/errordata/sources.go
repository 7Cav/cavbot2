package errordata

import (
	"errors"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"example.com/errtext"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Each way data leaves an error, reported where it leaves when it reaches a
// reply.

func Status(err error) {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) {
		utils.HandleError(nil, nil, "❌ Discord said "+strconv.Itoa(restErr.Response.StatusCode)) // want ""
	}
}

func MethodValue(err error) {
	f := err.Error // want ""
	utils.HandleError(nil, nil, f())
}

type page struct {
	Err error
}

func mustParse(text string) *template.Template {
	t, err := template.New("page").Parse(text)
	if err != nil {
		panic(err)
	}
	return t
}

var pageTemplate = mustParse("{{.Err}}")

func Rendered(err error) {
	var b strings.Builder
	_ = pageTemplate.Execute(&b, page{Err: err}) // want ""
	utils.HandleError(nil, nil, b.String())
}

type coded interface {
	Code() int
}

func Coded(err error) {
	var c coded
	if errors.As(err, &c) { // want ""
		utils.HandleError(nil, nil, "❌ Discord code "+strconv.Itoa(c.Code()))
	}
}

func AssertedStringer(err error) {
	if s, ok := err.(fmt.Stringer); ok { // want ""
		utils.HandleError(nil, nil, s.String())
	}
}

func Recovered() {
	defer func() {
		if r := recover(); r != nil { // want ""
			utils.HandleError(nil, nil, fmt.Sprint(r))
		}
	}()
}

func Described(err error) {
	utils.HandleError(nil, nil, errtext.Describe(err)) // want ""
}

func show[T any](v T) string { return fmt.Sprint(v) }

func Shown(err error) {
	utils.HandleError(nil, nil, show(err)) // want ""
}

func DescribedBy(describe func(error) string, err error) {
	utils.HandleError(nil, nil, describe(err)) // want ""
}
