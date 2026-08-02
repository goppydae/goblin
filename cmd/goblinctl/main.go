package main

import (
	"errors"
	"fmt"
	"os"

	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/cli"
)

func main() {
	// RunRoot, not Execute: a bare `goblinctl` must print help and exit
	// NON-ZERO. Cobra cannot do that for a root with no RunE - it returns
	// flag.ErrHelp, which ExecuteC catches, prints help for, and reports
	// as success, so the process exits 0.
	//
	// goblind was moved to RunRoot by GOBLIN-DIV-053; goblinctl was left
	// on Execute and so exited 0 on a bare invocation, measured here at
	// rc=0 while its peer returned 1. The contract puts this rule on the
	// control role as much as the daemon role.
	if err := gapicli.RunRoot(cli.RootCmd, os.Args[1:]); err != nil {
		if !errors.Is(err, gapicli.ErrNoCommand) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
