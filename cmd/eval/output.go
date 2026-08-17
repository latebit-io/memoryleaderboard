package main

import (
	"fmt"
	"io"
)

func writef(writer io.Writer, format string, values ...any) {
	_, _ = fmt.Fprintf(writer, format, values...)
}

func writeLine(writer io.Writer, values ...any) {
	_, _ = fmt.Fprintln(writer, values...)
}

func writeText(writer io.Writer, values ...any) {
	_, _ = fmt.Fprint(writer, values...)
}
