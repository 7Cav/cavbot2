package replies

import (
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
)

func formatsTheError(err error) {
	utils.HandleError(nil, nil, fmt.Sprintf("❌ Failed to fetch milpac: %v", err)) // want "."
}

func buildsTheMessageFirst(err error, verbose bool) {
	msg := "❌ Failed to fetch roster"
	if verbose {
		msg += ": " + err.Error()
	}
	utils.HandleError(nil, nil, msg) // want "."
}

func describe(what, detail string) string {
	return fmt.Sprintf("❌ Failed to fetch %s (%s)", what, detail)
}

func asksAHelper(err error) {
	utils.HandleError(nil, nil, describe("milpac", err.Error())) // want "."
}

const fetchFailed = "❌ Couldn't get that from the 7Cav API. Try again in a few minutes."

func sendsAFixedMessage(err error) {
	_ = err
	utils.HandleError(nil, nil, fetchFailed)
}

func namesWhatTheMemberTyped(department string) {
	utils.HandleError(nil, nil, fmt.Sprintf("⚠️ The %s roster came back empty.", department))
}

func emptyRosterMessage(position string) string {
	return fmt.Sprintf("❌ No troopers found for %q.", position)
}

func asksAHelperAboutMemberInput(position string) {
	utils.HandleError(nil, nil, emptyRosterMessage(position))
}

func classify(err error) string {
	if err == nil {
		return "❌ Nothing went wrong."
	}
	return "❌ Discord returned a server error."
}

func asksAHelperThatKeepsTheErrorOut(err error) {
	utils.HandleError(nil, nil, classify(err))
}

func replyWith(message string) {
	utils.HandleError(nil, nil, message)
}

func goesThroughAWrapper(err error) {
	replyWith("❌ Failed: " + err.Error()) // want "."
}

func goesThroughAWrapperWithAFixedMessage() {
	replyWith(fetchFailed)
}

func assemblesTheMessage(err error) {
	var sb strings.Builder
	sb.WriteString("❌ Failed: ")
	sb.WriteString(err.Error())
	utils.HandleError(nil, nil, sb.String()) // want "."
}

type failure struct {
	detail string
}

func keepsTheMessageInAField(err error) {
	f := failure{detail: "❌ Failed: " + err.Error()}
	utils.HandleError(nil, nil, f.detail) // want "."
}

func sharesTheMessageWithAClosure(err error) {
	msg := "❌ Failed: " + err.Error()
	reply := func() {
		utils.HandleError(nil, nil, msg) // want "."
	}
	reply()
}
