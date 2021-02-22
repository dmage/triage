package types

import "fmt"

type Build struct {
	Job       string
	BuildID   string
	GCSBucket string
	GCSPrefix string
}

func (b Build) String() string {
	return fmt.Sprintf("%s @ %s (gs://%s/%s)", b.Job, b.BuildID, b.GCSBucket, b.GCSPrefix)
}
