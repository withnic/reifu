package main

import (
	"os"

	"github.com/withnic/reifu/internal/app"
)

func main() {
	cli := app.CLI{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(int(cli.Run(os.Args[1:])))
}
