package main

import "github.com/FMNSSun/plus-debug/pdbg"
import "plus"
import "net"
import "fmt"
import "os"

func main() {
	server(os.Args[1])
}

func server(laddr string) {
	packetConn, err := net.ListenPacket("udp", laddr)

	if err != nil {
		panic("Could not create packet connection!")
	}


	connectionManager := PLUS.NewConnectionManager(packetConn)
	go connectionManager.Listen()

	for {
		connection := connectionManager.Accept()
		fmt.Println("Got connection")

		go func() {
			p := pdbg.NewPDbg(connection)
			for i := 0; i < 8; i++ {
				q := byte(i % 256)
				p.Write([]byte{q, 1, q, 2, q})
			}
		}()
	}
}
