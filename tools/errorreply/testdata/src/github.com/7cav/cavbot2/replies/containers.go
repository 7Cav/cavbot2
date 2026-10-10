package replies

import (
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
)

func keepsWhatFollowsTheErrorsFirstColon(err error) {
	utils.HandleError(nil, nil, "❌ Failed: "+strings.Split(err.Error(), ": ")[1]) // want "."
}

func repliesWithEachPartOfTheError(err error) {
	for _, part := range strings.Fields(err.Error()) {
		utils.HandleError(nil, nil, "❌ Failed: "+part) // want "."
	}
}

func readsTheErrorOutOfAnArray(err error) {
	parts := [2]string{"❌ Failed", err.Error()}
	utils.HandleError(nil, nil, parts[1]) // want "."
}

func failureAndCause(err error) [2]string {
	return [2]string{"❌ Failed", err.Error()}
}

func readsTheErrorOutOfAReturnedArray(err error) {
	utils.HandleError(nil, nil, failureAndCause(err)[1]) // want "."
}

func repliesWithEachRuneOfTheError(err error) {
	for _, r := range err.Error() {
		utils.HandleError(nil, nil, "❌ Failed: "+string(r)) // want "."
	}
}

func readsTheErrorOutOfAMapByKey(err error) {
	failures := map[string]string{"roster": err.Error()}
	utils.HandleError(nil, nil, "❌ Failed: "+failures["roster"]) // want "."
}

func readsTheErrorOutOfAMapWithCommaOk(err error) {
	failures := map[string]string{"roster": err.Error()}
	if failure, ok := failures["roster"]; ok {
		utils.HandleError(nil, nil, "❌ Failed: "+failure) // want "."
	}
}

func repliesWithEachKeyOfAMapOfErrors(err error) {
	seen := map[string]bool{err.Error(): true}
	for failure := range seen {
		utils.HandleError(nil, nil, "❌ Failed: "+failure) // want "."
	}
}

func repliesWithAWholeMapOfErrors(err error) {
	failures := map[string]string{"roster": err.Error()}
	utils.HandleError(nil, nil, fmt.Sprint("❌ Failed: ", failures)) // want "."
}

func looksUpAFixedMessageByTheErrorsText(err error) {
	phrases := map[string]string{"HTTP 403 Forbidden": "❌ The bot is missing a permission."}
	utils.HandleError(nil, nil, phrases[err.Error()])
}

func assertsTheErrorsTextOutOfAnInterface(err error) {
	var detail any = err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+detail.(string)) // want "."
}

func assertsTheErrorsTextOutOfAnInterfaceWithCommaOk(err error) {
	var detail any = err.Error()
	if text, ok := detail.(string); ok {
		utils.HandleError(nil, nil, "❌ Failed: "+text) // want "."
	}
}

func saysWhetherTheErrorsTextIsAStringer(err error) {
	var detail any = err.Error()
	_, ok := detail.(fmt.Stringer)
	utils.HandleError(nil, nil, fmt.Sprint("❌ Failed. Stringer: ", ok))
}

func readsTheErrorOutOfAMadeSlice(err error) {
	lines := make([]string, 2)
	lines[1] = err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+lines[1]) // want "."
}

func joinsAMadeSliceHoldingTheError(err error) {
	lines := make([]string, 2)
	lines[0] = "❌ Failed:"
	lines[1] = err.Error()
	utils.HandleError(nil, nil, strings.Join(lines, " ")) // want "."
}

func picksAFixedMessageByAnIndexFromTheError(err error) {
	messages := []string{"❌ Discord refused that.", "❌ Discord is down."}
	utils.HandleError(nil, nil, messages[len(err.Error())%2])
}

func receivesTheErrorsTextOnAChannel(err error) {
	failures := make(chan string, 1)
	failures <- err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+<-failures) // want "."
}

func selectsTheErrorsTextOffAChannel(err error, done chan string) {
	failures := make(chan string, 1)
	failures <- err.Error()
	select {
	case failure := <-failures:
		utils.HandleError(nil, nil, "❌ Failed: "+failure) // want "."
	case <-done:
	}
}

func readsTheErrorOutOfASliceSizedAtRunTime(err error, names []string) {
	lines := make([]string, len(names)+1)
	lines[0] = err.Error()
	utils.HandleError(nil, nil, "❌ Failed: "+lines[0]) // want "."
}
