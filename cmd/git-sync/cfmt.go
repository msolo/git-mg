package main

import (
	"flag"
	"fmt"
)

var (
	verbose bool
	quiet   bool
)

func RegisterFlags(fs *flag.FlagSet) {
	fs.BoolVar(&verbose, "v", false, "Enable more console output")
	fs.BoolVar(&quiet, "q", false, "Enable less console output")
}

func VerbosePrintf(msg string, args ...interface{}) {
	if verbose {
		fmt.Printf(msg, args...)
	}
}

func NoisyPrintf(msg string, args ...interface{}) {
	if !quiet {
		fmt.Printf(msg, args...)
	}
}
