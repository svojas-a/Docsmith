package main

import (
    "fmt"
    "os"
)

func main() {
    msg := os.Getenv("GREET_MSG")
    if msg == "" {
        msg = "Hello from Docksmith!"
    }
    fmt.Println(msg)
}
