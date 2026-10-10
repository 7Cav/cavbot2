package replies

import "github.com/7cav/cavbot2/utils"

func repliesWithTheErrorsFirstBytesAsAnArray(err error) {
	first := [4]byte([]byte(err.Error()))
	utils.HandleError(nil, nil, "❌ Failed: "+string(first[:])) // want "Discord"
}
