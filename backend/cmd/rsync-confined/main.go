package main

import (
	"errors"
	"fmt"
	"os"

	"xirang/backend/internal/rsyncconfinement"
)

func main() {
	if err := rsyncconfinement.RunHelper(os.Args[1:]); err != nil {
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			code := exitCoder.ExitCode()
			if code < 0 {
				code = 126
			}
			os.Exit(code)
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
}
