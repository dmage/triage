package testname

import (
	"regexp"
	"strings"
)

var (
	suiteRe   = regexp.MustCompile(`\[Suite:[^]]*\]`)
	skippedRe = regexp.MustCompile(`\[Skipped:[^]]*\]`)
	spacesRe  = regexp.MustCompile(`[ \t]+`)
)

func Normalize(x string) string {
	x = suiteRe.ReplaceAllString(x, "")
	x = skippedRe.ReplaceAllString(x, "")
	x = spacesRe.ReplaceAllString(x, " ")
	x = strings.TrimSpace(x)
	return x
}
