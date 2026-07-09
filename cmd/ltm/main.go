package main

import (
	"fmt"
	"os"

	"ltm/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ltm: "+err.Error())
		os.Exit(1)
	}
}
