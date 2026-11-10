package main

import (
	"fmt"
	"os"
	_ "time/tzdata" // Keep named timezones available without system zoneinfo files.

	"github.com/gbdubs/pharos/internal/archive"
)

func main() {
	stopFeedbackLogs := archive.CaptureFeedbackServiceLogs()
	defer stopFeedbackLogs()
	if err := archive.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		stopFeedbackLogs()
		os.Exit(1)
	}
}
