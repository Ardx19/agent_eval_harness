// Command toolstub is installed setuid to the tool uid and symlinked under each
// tool's documented name. It signs the call and forwards it to the host.
package main

import (
	"io"
	"os"
	"syscall"

	"agentevalharness/internal/guest"
	"agentevalharness/internal/stub"
	"agentevalharness/internal/toolproto"
)

const hostCID, toolboxPort = 2, 5001

func main() {
	fi, _ := os.Stdin.Stat()
	tty := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	os.Exit(stub.Run(stub.Env{
		Argv: os.Args, Stdin: os.Stdin, StdinTTY: tty, Stdout: os.Stdout, Stderr: os.Stderr,
		PID: os.Getpid(), PPID: os.Getppid(),
		KeyPath: toolproto.KeyPath, ConfGlob: toolproto.ConfGlob,
		Dial: func() (io.ReadWriteCloser, error) { return guest.DialVsock(hostCID, toolboxPort) },
		DropToCaller: func() error {
			ruid := syscall.Getuid()
			if err := syscall.Setresuid(-1, ruid, -1); err != nil {
				return err
			}
			if syscall.Geteuid() != ruid {
				return syscall.EPERM
			}
			return nil
		},
	}))
}
