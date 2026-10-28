// Command mkbox is the makebox command line entry point.
package main

import (
	"os"

	"github.com/makebox/makebox/internal/cli"
	"github.com/makebox/makebox/internal/i18n"
	"github.com/makebox/makebox/internal/logx"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.Execute(cli.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		logx.Errorf("%s", i18n.Translate(err.Error()))
		os.Exit(cli.ExitCode(err))
	}
}
