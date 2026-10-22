package main

import (
	"flag"
	"os"
)

// newFlagSet builds a flag set that reports errors without exiting the
// process, so that the caller controls the exit code.
func newFlagSet(name string, stderr *os.File) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
