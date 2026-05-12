package utils

import (
	"fmt"
	"regexp"
)

var uniformURLRe = regexp.MustCompile(`/\d+/(\d+)\.jpg`)

// ExtractMilpacIDFromUniformURL pulls the milpac ID out of a 7Cav uniform
// image URL. The expected shape is `…/<anything>/<milpacID>.jpg`. Returns an
// error if the URL doesn't match — callers surface that to the user as
// "Failed to parse uniform URL".
func ExtractMilpacIDFromUniformURL(url string) (string, error) {
	matches := uniformURLRe.FindStringSubmatch(url)
	if len(matches) < 2 {
		return "", fmt.Errorf("uniform URL %q did not match expected /N/M.jpg pattern", url)
	}
	return matches[1], nil
}
