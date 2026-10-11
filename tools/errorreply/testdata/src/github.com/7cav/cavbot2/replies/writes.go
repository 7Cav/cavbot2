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

func repliesFromAMapItWroteTheErrorIntoWhileAClosureReadsIt(err error) {
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

// roster is a lookup that keeps why it failed.
type roster struct{ failure string }

func (r *roster) describe() string { return r.failure }

func repliesWithWhatAMethodValueReturns(err error) {
	r := &roster{failure: err.Error()}
	describe := r.describe
	utils.HandleError(nil, nil, "❌ Failed: "+describe()) // want "Discord"
}

func repliesWithFixedTextAMethodValueReturns() {
	r := &roster{failure: fetchFailed}
	describe := r.describe
	utils.HandleError(nil, nil, describe())
}

// drafts holds a builder for each reply a command sends.
type drafts struct{ replies []strings.Builder }

func repliesWithWhatWasWrittenIntoABuilderItsDraftsHold(err error) {
	d := &drafts{replies: make([]strings.Builder, 1)}
	d.replies[0].WriteString(err.Error())
	utils.HandleError(nil, nil, "❌ Failed: "+d.replies[0].String()) // want "Discord"
}

// draft keeps the builder a reply is written into.
type draft struct{ b *strings.Builder }

func writeThroughADraft(b *strings.Builder, err error) {
	d := draft{b: b}
	d.b.WriteString(err.Error())
}

func repliesWithWhatAHelperWroteThroughTheDraftItKeptTheBuilderIn(err error) {
	var b strings.Builder
	writeThroughADraft(&b, err)
	utils.HandleError(nil, nil, "❌ Failed: "+b.String()) // want "Discord"
}

func writeFixedFailure(w io.Writer, err error) {
	io.WriteString(w, fetchFailed)
	log.Print(err)
}

func repliesWithWhatAHelperWroteIntoTheWriterNextToTheErrorItLogged(err error) {
	var b strings.Builder
	writeFixedFailure(&b, err)
	utils.HandleError(nil, nil, b.String())
}

// notes collects the lines of a reply.
type notes struct{ lines []string }

func (n *notes) add(line string) { n.lines = append(n.lines, line) }

// page holds the notes a reply is built from.
type page struct{ notes *notes }

func repliesWithWhatAMethodValueOfItsNotesAdded(err error) {
	p := &page{notes: &notes{}}
	add := p.notes.add
	add(err.Error())
	utils.HandleError(nil, nil, "❌ Failed: "+strings.Join(p.notes.lines, " ")) // want "Discord"
}
