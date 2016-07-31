package platform

import (
	"runtime"

	"github.com/2qif49lt/logrus"
)

var (
	// Architecture holds the runtime architecture of the process.
	Architecture string
	// OSType holds the runtime operating system type (Linux, …) of the process.
	OSType string
)

func init() {
	var err error
	Architecture, err = runtimeArchitecture()
	if err != nil {
		logrus.Errorf("Could not read system architecture info: %v", err)
	}
	OSType = runtime.GOOS
}
