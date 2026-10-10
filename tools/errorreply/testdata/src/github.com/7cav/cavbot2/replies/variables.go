package replies

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
)

var lastFailure string

func keepsTheErrorInAVariableAndRepliesWithIt(err error) {
	lastFailure = err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+lastFailure) // want "."
}

var sweepFailure string

func recordsTheSweepsError(err error) {
	sweepFailure = err.Error()
}

func repliesWithTheRecordedSweepError() {
	utils.HandleError(nil, nil, "❌ The last sweep failed: "+sweepFailure) // want "."
}

var failures []string

func appendsTheError(err error) {
	failures = append(failures, err.Error())
}

func repliesWithTheFirstFailure() {
	utils.HandleError(nil, nil, "❌ Failed: "+failures[0]) // want "."
}

var failuresByCommand = map[string]string{}

func setsTheErrorInAMap(err error) {
	failuresByCommand["roster"] = err.Error()
}

func repliesWithTheRostersFailure() {
	utils.HandleError(nil, nil, "❌ Failed: "+failuresByCommand["roster"]) // want "."
}

var lastWait time.Duration

func keepsTheWaitParsedFromTheError(err error) {
	lastWait, _ = time.ParseDuration(err.Error())
}

func namesTheKeptWait() {
	utils.HandleError(nil, nil, fmt.Sprintf("❌ Discord is busy. Try again in %s.", lastWait))
}

func namesAWaitReadOutOfASlice(err error) {
	wait, _ := time.ParseDuration(err.Error())
	waits := []time.Duration{wait}
	utils.HandleError(nil, nil, fmt.Sprintf("❌ Discord is busy. Try again in %s.", waits[0]))
}

var busyMessage = "❌ Discord is busy. Try again in a few minutes."

func repliesWithAFixedMessageKeptInAVariable() {
	utils.HandleError(nil, nil, busyMessage)
}

var recentFailures = make([]string, 1)

func setsTheErrorAsTheRecentFailure(err error) {
	recentFailures[0] = err.Error()
}

func repliesWithTheRecentFailure() {
	utils.HandleError(nil, nil, "❌ Failed: "+recentFailures[0]) // want "."
}

var errStartup = errors.New("HTTP 401 Unauthorized")

var startupFailure = "❌ The bot couldn't start: " + errStartup.Error()

func repliesWithTheStartupFailure() {
	utils.HandleError(nil, nil, startupFailure) // want "."
}

var sweep struct {
	lastFailure string
}

func recordsTheSweepsErrorInAField(err error) {
	sweep.lastFailure = err.Error()
}

func repliesWithTheSweepsRecordedField() {
	utils.HandleError(nil, nil, "❌ The last sweep failed: "+sweep.lastFailure) // want "."
}

var lastRefusal string

func rememberRefusal(reason string) {
	lastRefusal = reason
}

func refusesWithTheError(err error) {
	rememberRefusal(err.Error())
}

func repliesWithTheRememberedRefusal() {
	utils.HandleError(nil, nil, "❌ Refused: "+lastRefusal) // want "."
}

var sweepFailures = make(chan string, 1)

func sendsTheErrorOnAVariablesChannel(err error) {
	sweepFailures <- err.Error()
}

func repliesWithAFailureReceivedFromAVariablesChannel() {
	utils.HandleError(nil, nil, "❌ The sweep failed: "+<-sweepFailures) // want "."
}

var lastFailures [2]string

func setsTheErrorThroughASliceOfAVariablesArray(err error) {
	failures := lastFailures[:]
	failures[0] = err.Error()
}

func repliesWithTheLastFailure() {
	utils.HandleError(nil, nil, "❌ Failed: "+lastFailures[0]) // want "."
}

var report strings.Builder

func writesTheErrorIntoAVariablesBuilder(err error) {
	report.WriteString(err.Error())
}

func repliesWithTheBuiltReport() {
	utils.HandleError(nil, nil, "❌ Failed: "+report.String()) // want "."
}
