package main

import (
	"fmt"
	"os"

	"docksmith/build"
	"docksmith/parser"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		fmt.Println("Usage: docksmith build -t <tag> <context>")
		return
	}

	switch args[1] {
	case "build":
		handleBuild()
	default:
		fmt.Println("Unknown command:", args[1])
	}
}

func handleBuild() {
	args := os.Args
	tag := "latest"
	contextDir := "."
	noCache := false

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "-t":
			if i+1 < len(args) {
				tag = args[i+1]
				i++
			}
		case "--no-cache":
			noCache = true
		default:
			contextDir = args[i]
		}
	}

	instructions, err := parser.ParseDocksmithfile(contextDir + "/Docksmithfile")
	if err != nil {
		fmt.Println("Error parsing Docksmithfile:", err)
		os.Exit(1)
	}

	err = build.Run(instructions, build.BuildOptions{
		Tag:     tag,
		Context: contextDir,
		NoCache: noCache,
	})
	if err != nil {
		fmt.Println("Build failed:", err)
		os.Exit(1)
	}
}
