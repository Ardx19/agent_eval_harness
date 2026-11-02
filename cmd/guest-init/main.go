// Command guest-init is PID 1 inside the microVM.
package main

import (
	"os"

	"agentevalharness/internal/guest"
)

func main() { os.Exit(guest.Supervise()) }
