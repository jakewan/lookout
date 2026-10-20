package main

import (
	"io"
	"log"
)

func read(_ []string, _ environment, _ io.Writer, diag *log.Logger) int {
	diag.Print("lookout read: not implemented")
	return exitFailure
}
