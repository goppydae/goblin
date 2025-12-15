package main

import (
	"log"

	"github.com/goppydae/goblin/internal/cli"
)

func main() {
	if err := cli.RootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
