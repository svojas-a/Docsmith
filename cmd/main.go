package main

import (
	"fmt"
	"os"
	"strings"

	"docksmith/parser"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: docksmith <command>")
		return
	}

	command := os.Args[1]

	switch command {

	case "build":
		handleBuild()

	case "run":
		fmt.Println("Run not implemented")

	case "images":
		fmt.Println("Images not implemented")

	case "rmi":
		fmt.Println("RMI not implemented")

	default:
		fmt.Println("Unknown command")
	}
}

func handleBuild() {
	args := os.Args

	// Default tag
	tag := "latest"

	// Parse -t flag
	for i := 0; i < len(args); i++ {
		if args[i] == "-t" && i+1 < len(args) {
			tag = args[i+1]
		}
	}

	fmt.Println("Building image with tag:", tag)

	// Parse Docksmithfile
	instructions, err := parser.ParseDocksmithfile("Docksmithfile")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Println("Parsed Instructions:")

	for i, inst := range instructions {
		fmt.Printf("%d. %s %s\n", i+1, inst.Type, strings.Join(inst.Args, " "))
	}
}
