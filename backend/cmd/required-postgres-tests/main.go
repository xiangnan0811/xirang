package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"xirang/backend/internal/requiredpostgres"
)

func runPhase(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	fmt.Fprintf(os.Stderr, "ci-phase {\"phase\":%q,\"elapsed_ms\":%d,\"ok\":%t}\n", name, time.Since(start).Milliseconds(), err == nil)
	return err
}

func main() {
	inventory := flag.String("inventory", "", "inventory JSON path")
	listOnly := flag.Bool("list-only", false, "validate inventory without executing tests")
	evidence := flag.String("evidence-dir", "", "new evidence directory")
	flag.Parse()
	if *inventory == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "inventory is required and positional arguments are not supported")
		os.Exit(2)
	}
	var inv requiredpostgres.Inventory
	err := runPhase("load", func() error { var err error; inv, err = requiredpostgres.Load(*inventory); return err })
	if err == nil {
		err = runPhase("census", func() error { return requiredpostgres.Census(".", inv) })
	}
	if err == nil {
		err = runPhase("native_list", func() error { return requiredpostgres.ValidateNative(inv) })
	}
	if err == nil && !*listOnly {
		err = runPhase("execute", func() error { return requiredpostgres.Run(inv, *evidence) })
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
