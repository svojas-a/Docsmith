package main

import (
	"fmt"
	"os"
)

func main() {
	message := os.Getenv("MESSAGE")
	if message == "" {
		message = "Default message"
	}
	fmt.Printf("%s\n", message)
	fmt.Println("This is the demo container app for the demo!")
}