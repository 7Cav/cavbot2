package errordata

import (
	"errors"
	"fmt"
	"strings"

	"example.com/errtext"

	"github.com/7cav/cavbot2/apierr"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The walk fails closed: a use it has no rule for, such as a channel send,
// counts as one that takes the data somewhere else.
func SentOnChannel(err error) {
	ch := make(chan string, 1)
	ch <- err.Error() // want ""
	utils.HandleError(nil, nil, "❌ Failed: "+<-ch)
}

func ClosureReads(err error) {
	msg := err.Error() // want ""
	func() { utils.HandleError(nil, nil, "❌ Failed: "+msg) }()
}

func OtherPackageType(err error) {
	var w *apierr.Wrapped
	if errors.As(err, &w) {
		utils.HandleError(nil, nil, w.Error()) // want ""
	}
}

// An error held by value in a map's element is read as a struct value.
func ByValue(fs map[string]discordgo.RESTError, id string) {
	utils.HandleError(nil, nil, "❌ "+string(fs[id].ResponseBody)) // want ""
}

var notify = utils.Warn

// PostInstead sets notify to a function that posts what it is handed to
// Discord, so notify reaches more than the log.
func PostInstead(s *discordgo.Session) {
	notify = func(msg string, args ...any) { _, _ = s.ChannelMessageSend("1", fmt.Sprint(args...)) }
}

func Notified(err error) {
	notify("lookup failed", "error", err) // want ""
}

type holder[T any] struct {
	v T
}

func (h holder[T]) Text() string { return fmt.Sprint(h.v) }

func Held(err error) {
	utils.HandleError(nil, nil, holder[error]{v: err}.Text()) // want ""
}

func WrittenTo(err error) {
	var b strings.Builder
	errtext.WriteTo(&b, err) // want ""
	utils.HandleError(nil, nil, b.String())
}

func Remembered2(err error) {
	var p errtext.Printer
	p.Remember(err) // want ""
	utils.HandleError(nil, nil, p.Last())
}

// append may write into the array of the slice it was handed, which its
// caller can read past the slice's length.
func logWith(kv []any, err error) {
	utils.Warn("lookup failed", append(kv, "error", err)...) // want ""
}

func DescribedAll(err, other error) {
	utils.HandleError(nil, nil, errtext.DescribeAll(err, other)) // want ""
}
