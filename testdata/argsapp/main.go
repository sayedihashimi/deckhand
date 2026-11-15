// argsapp stands in for claude.exe in deckhand's e2e tests: it prints each
// argument on its own line, then waits so the screen can be read.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		fmt.Printf("arg: [%s]\n", a)
	}
	fmt.Println("argsapp done")
	time.Sleep(10 * time.Minute)
}
