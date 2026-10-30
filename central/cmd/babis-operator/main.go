// Command babis-operator is the central Babis fleet operator.
package main

import "fmt"

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	fmt.Println("babis-operator", Version)
}
