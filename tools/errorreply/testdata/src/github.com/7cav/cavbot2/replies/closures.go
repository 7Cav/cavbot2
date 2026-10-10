package replies

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/7cav/cavbot2/utils"
)

func repliesWithWhatAClosureCalledAtOnceKept(err error) {
	var reason string
	func() { reason = err.Error() }()
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func repliesWithAPhraseAClosureChoseByCheckingTheError(err error) {
	reason := "❌ Couldn't fetch the roster."
	func() {
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "❌ The 7Cav API took too long. Try again in a few minutes."
		}
	}()
	utils.HandleError(nil, nil, reason)
}

func repliesWithWhatAClosureCalledThroughAVariableKept(err error) {
	var reason string
	keep := func() { reason = err.Error() }
	keep()
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func repliesWithWhatAClosureRunOnceKept(err error) {
	var once sync.Once
	var reason string
	once.Do(func() { reason = err.Error() })
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func run(fn func()) {
	fn()
}

func repliesWithWhatAClosurePassedOnKept(err error) {
	var reason string
	run(func() { reason = err.Error() })
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func repliesWithWhatAGoroutineSent(err error) {
	failures := make(chan string, 1)
	go func() { failures <- err.Error() }()
	utils.HandleError(nil, nil, "❌ Failed: "+<-failures) // want "Discord"
}

func repliesWithAMapValueAClosureSet(err error) {
	failures := map[string]string{}
	func() { failures["roster"] = err.Error() }()
	utils.HandleError(nil, nil, "❌ Failed: "+failures["roster"]) // want "Discord"
}

func repliesWithWhatAClosureSetInAMapItWasGiven(failures map[string]string, err error) {
	func() { failures["roster"] = err.Error() }()
	utils.HandleError(nil, nil, "❌ Failed: "+failures["roster"]) // want "Discord"
}

func joinsWhatAClosureAppended(err error) {
	var lines []string
	func() { lines = append(lines, err.Error()) }()
	utils.HandleError(nil, nil, "❌ Failed: "+strings.Join(lines, " ")) // want "Discord"
}

func repliesWithWhatAClosureWroteIntoABuilder(err error) {
	var sb strings.Builder
	func() { sb.WriteString(err.Error()) }()
	utils.HandleError(nil, nil, "❌ Failed: "+sb.String()) // want "Discord"
}

func repliesWithAFieldAClosureSet(err error) {
	var f failure
	func() { f.detail = err.Error() }()
	utils.HandleError(nil, nil, f.detail) // want "Discord"
}

func repliesWithAFieldAClosureSetThroughAPointer(err error) {
	f := &failure{}
	func() { f.detail = err.Error() }()
	utils.HandleError(nil, nil, f.detail) // want "Discord"
}

func repliesWithWhatANestedClosureKept(err error) {
	var reason string
	func() {
		func() { reason = err.Error() }()
	}()
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func repliesFromAClosureWithWhatItsSiblingKept(err error) {
	var reason string
	keep := func() { reason = err.Error() }
	reply := func() {
		utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
	}
	keep()
	reply()
}

func describeOnTheWayOut(err error) (reason string) {
	defer func() { reason = err.Error() }()
	return "❌ Failed."
}

func repliesWithWhatADeferredClosureSet(err error) {
	utils.HandleError(nil, nil, describeOnTheWayOut(err)) // want "Discord"
}

func repliesWithWhatAClosureKeptOfItsArgument(err error) {
	var reason string
	keep := func(s string) string {
		reason = s
		return reason
	}
	utils.HandleError(nil, nil, "❌ Failed: "+keep(err.Error())) // want "Discord"
}

func repliesWithFixedTextAClosureKeptOfItsArgument() {
	var reason string
	keep := func(s string) { reason = s }
	keep(fetchFailed)
	utils.HandleError(nil, nil, reason)
}

func repliesWithWhatAClosureWroteThroughAPointer(err error) {
	var reason string
	p := &reason
	func() { *p = err.Error() }()
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func repliesWithFixedTextAClosureWroteThroughAPointer(err error) {
	reason := "❌ Couldn't fetch the roster."
	p := &reason
	func() {
		if errors.Is(err, context.DeadlineExceeded) {
			*p = "❌ The 7Cav API took too long. Try again in a few minutes."
		}
	}()
	utils.HandleError(nil, nil, reason)
}

func repliesWithFixedTextWhileAClosureReadsTheErrorThroughAPointer(err error) {
	reply := "❌ Couldn't fetch the roster."
	var detail string
	shown := &reply
	if err != nil {
		shown = &detail
		detail = err.Error()
	}
	func() { log.Print(*shown) }()
	utils.HandleError(nil, nil, reply)
}

func repliesWithFixedTextAfterSwappingCapturedPointers() {
	var a, b string
	p, q := &a, &b
	p, q = q, p
	*q = fetchFailed
	func() { _, _ = p, q }()
	utils.HandleError(nil, nil, a)
}

func repliesWithWhatWasWrittenThroughAPointerToAPointer(err error) {
	var reason string
	p := &reason
	q := &p
	**q = err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}

func quoteLater(msg string) string {
	return func() string { return msg }()
}

func repliesWithWhatAHelpersClosureReturned(err error) {
	utils.HandleError(nil, nil, "❌ Failed: "+quoteLater(err.Error())) // want "Discord"
}

func repliesWithFixedTextAHelpersClosureReturned() {
	utils.HandleError(nil, nil, quoteLater(fetchFailed))
}

func quoteMuchLater(msg string) string {
	return func() string {
		return func() string { return msg }()
	}()
}

func repliesWithWhatAHelpersNestedClosuresReturned(err error) {
	utils.HandleError(nil, nil, "❌ Failed: "+quoteMuchLater(err.Error())) // want "Discord"
}

func repliesWithFixedTextAHelpersNestedClosuresReturned() {
	utils.HandleError(nil, nil, quoteMuchLater(fetchFailed))
}

func replyThroughAClosure(message string) {
	utils.HandleError(nil, nil, func() string { return message }())
}

func repliesThroughAWrapperThatReadsItInAClosure(err error) {
	replyThroughAClosure("❌ Failed: " + err.Error()) // want "Discord"
}

func repliesWithFixedTextThroughAWrapperThatReadsItInAClosure() {
	replyThroughAClosure(fetchFailed)
}

func keptByAClosure(msg string) string {
	var reason string
	keep := func(s string) { reason = s }
	keep(msg)
	return reason
}

func repliesWithWhatAHelpersClosureKept(err error) {
	utils.HandleError(nil, nil, "❌ Failed: "+keptByAClosure(err.Error())) // want "Discord"
}

func reasonLater(msg string) func() string { return func() string { return msg } }

func repliesWithWhatAReturnedClosureGivesBack(err error) {
	utils.HandleError(nil, nil, "❌ Failed: "+reasonLater(err.Error())()) // want "Discord"
}

func repliesWithFixedTextAReturnedClosureGivesBack() {
	utils.HandleError(nil, nil, "❌ Failed: "+reasonLater(fetchFailed)())
}

func applyTo(keep func(string), s string) { keep(s) }

func repliesWithWhatAClosureHandedToAHelperKept(err error) {
	var reason string
	applyTo(func(s string) { reason = s }, err.Error())
	utils.HandleError(nil, nil, "❌ Failed: "+reason) // want "Discord"
}
