package main

import (
	"fmt"
	"os"
)

func main() {
	greeting := os.Getenv("GREETING")
	if greeting == "" {
		greeting = "Hello"
	}
	fmt.Printf("%s from Docksmith!\n", greeting)
	fmt.Println("Container is fully isolated — host filesystem not visible.")
}
// change
