package main

import (
	"fmt"
	"os"

	"github.com/goppydae/goblin/internal/cli"
)

func main() {
	if err := cli.RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
