// Command guest-fwd runs inside the microVM and forwards 127.0.0.1:PORT to the
// host model proxy over vsock.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"agentevalharness/internal/guest"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "loopback address agents use as their base URL")
	cid := flag.Uint("cid", 2, "vsock CID of the host")
	port := flag.Uint("port", 5000, "vsock port of the host model proxy")
	flag.Parse()

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "guest-fwd:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	dial := func() (guest.Conn, error) { return guest.DialVsock(uint32(*cid), uint32(*port)) }
	if err := guest.Forward(ctx, l, dial); err != nil {
		fmt.Fprintln(os.Stderr, "guest-fwd:", err)
		os.Exit(1)
	}
}
