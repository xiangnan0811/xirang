package main

import (
	"flag"
	"fmt"
	"os"

	"xirang/backend/internal/requiredpostgres"
)

func main() {
	inventory := flag.String("inventory", "", "inventory JSON path")
	listOnly := flag.Bool("list-only", false, "validate inventory without executing tests")
	evidence := flag.String("evidence-dir", "", "new evidence directory")
	flag.Parse()
	if *inventory == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "inventory is required and positional arguments are not supported")
		os.Exit(2)
	}
	inv, err := requiredpostgres.Load(*inventory)
	if err == nil {
		err = requiredpostgres.Census(".", inv)
	}
	if err == nil {
		err = requiredpostgres.ValidateNative(inv)
	}
	if err == nil && !*listOnly {
		err = requiredpostgres.Run(inv, *evidence)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
