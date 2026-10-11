package replies

import (
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/7cav/cavbot2/utils"
)

func repliesWithWhatAClosureWroteIntoACapturedBuilder(err error) {
	sb := &strings.Builder{}
	func() { sb.WriteString(err.Error()) }()
	utils.HandleError(nil, nil, "❌ Failed: "+sb.String()) // want "Discord"
}

func repliesFromABuilderItWasGivenAfterAClosureWroteIntoIt(sb *strings.Builder, err error) {
	func() { sb.WriteString(err.Error()) }()
	utils.HandleError(nil, nil, "❌ Failed: "+sb.String())
}

func put(m map[string]string, v string) { m["roster"] = v }

func fill(lines []string, v string) { lines[0] = v }

func repliesWithWhatADeferredClosureWroteIntoACapturedMap(err error) {
	m := map[string]string{}
	defer func() { utils.HandleError(nil, nil, "❌ Failed: "+m["roster"]) }() // want "Discord"
	defer func() { put(m, err.Error()) }()
}

func repliesWithWhatAClosureCalledLaterWroteIntoACapturedSlice(err error) {
	lines := make([]string, 1)
	keep := func() { fill(lines, err.Error()) }
	keep()
	utils.HandleError(nil, nil, "❌ Failed: "+lines[0]) // want "Discord"
}

func repliesFromAMapAClosureReadsAfterWritingTheErrorIntoIt(err error) {
	m := map[string]string{}
	put(m, err.Error())
	defer func() { _ = len(m) }()
	utils.HandleError(nil, nil, "❌ Failed: "+m["roster"]) // want "Discord"
}

func logFailures(failures map[string]string, err error) {
	log.Printf("%d failures before %v", len(failures), err)
}

func repliesFromAMapAClosureLoggedNextToTheError(err error) {
	failures := map[string]string{"roster": fetchFailed}
	func() { logFailures(failures, err) }()
	utils.HandleError(nil, nil, failures["roster"])
}

type patrol struct {
	stop func()
	note string
}

func every(fn func()) func() { return fn }

func (s *patrol) schedule() { s.stop = every(s.schedule) }

func repliesFromWhatSchedulesItselfThroughAMethodValue() {
	s := &patrol{note: fetchFailed}
	s.stop = every(s.schedule)
	utils.HandleError(nil, nil, s.note)
}

func repliesWithWhatFprintfWroteIntoABuilder(err error) {
	var b strings.Builder
	fmt.Fprintf(&b, "❌ Failed: %v", err)
	utils.HandleError(nil, nil, b.String()) // want "Discord"
}

func repliesWithWhatAClosureFprintedIntoACapturedBuilder(err error) {
	b := &strings.Builder{}
	func() { fmt.Fprintf(b, "❌ Failed: %v", err) }()
	utils.HandleError(nil, nil, b.String()) // want "Discord"
}

func writeFailure(w io.Writer, err error) {
	io.WriteString(w, "❌ Failed: "+err.Error())
}

func repliesWithWhatAHelperWroteIntoTheWriterItWasHanded(err error) {
	var b strings.Builder
	writeFailure(&b, err)
	utils.HandleError(nil, nil, b.String()) // want "Discord"
}

func writeFailureBytes(w io.Writer, err error) {
	w.Write([]byte("❌ Failed: " + err.Error()))
}

func repliesWithWhatAHelperWroteThroughTheWritersMethod(err error) {
	var b strings.Builder
	writeFailureBytes(&b, err)
	utils.HandleError(nil, nil, b.String()) // want "Discord"
}

func repliesWithAWordScannedOutOfTheError(err error) {
	var word string
	fmt.Sscan(err.Error(), &word)
	utils.HandleError(nil, nil, "❌ Failed: "+word) // want "Discord"
}

func repliesWithWhoJoined(name string) {
	var b strings.Builder
	fmt.Fprintf(&b, "✅ %s joined", name)
	utils.HandleError(nil, nil, b.String())
}
