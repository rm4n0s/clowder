package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rm4n0s/clowder/internal/config"
	"github.com/rm4n0s/errors"
)

// runDemo prepares the data directory of the demo. The directory is made if it
// is missing and must otherwise be empty, so that demo never touches the data
// of a real instance. It prints one line to stdout when the directory is ready.
// TODO(step 2.2): fill the database with the demo users; later steps add
// their own demo data.
func runDemo(args []string, env func(string) string, stdout, stderr io.Writer) error {
	var dataDir string
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&dataDir, "data", envString(env, "CLOWDER_DATA", config.DefaultDataDir),
		"data directory, made if missing and empty (CLOWDER_DATA)")
	if err := flags.Parse(args); err != nil {
		return errors.NewErr(tagFlagsInvalid, err)
	}
	if flags.NArg() > 0 {
		return errors.New(tagFlagsInvalid, "unexpected arguments", "args", flags.Args())
	}
	if dataDir == "" {
		return errors.New(tagFlagsInvalid, "data directory is empty", "flag", "data")
	}

	if err := os.MkdirAll(dataDir, dataDirMode); err != nil {
		return errors.NewErr(tagDataDirCreateFailed, err).SetMetadata("dir", dataDir)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return errors.NewErr(tagDataDirReadFailed, err).SetMetadata("dir", dataDir)
	}
	if len(entries) > 0 {
		return errors.New(tagDataDirNotEmpty, "the demo needs an empty data directory",
			"dir", dataDir, "entries", len(entries))
	}
	_, _ = fmt.Fprintf(stdout, "demo data directory is ready: %s\n", dataDir)
	return nil
}

const (
	tagDataDirNotEmpty   = "DataDirNotEmpty"
	tagDataDirReadFailed = "DataDirReadFailed"
)
