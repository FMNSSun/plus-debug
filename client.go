package main

import "github.com/FMNSSun/plus-debug/pdbg"
import "plus"
import "net"
//import "fmt"
import "os"

func main() {
	client(os.Args[1], os.Args[2])
}

func client(laddr string, raddr string) {
	packetConn, err := net.ListenPacket("udp", laddr)

	if err != nil {
		panic("Could not create packet connection!")
	}

	udpAddr, err := net.ResolveUDPAddr("udp4", raddr)

	connectionManager, connection := PLUS.NewConnectionManagerClient(packetConn, 1989, udpAddr)
	go connectionManager.Listen()

	p := pdbg.NewPDbg(connection)

	buffer := make([]byte, 1024)

	p.Write(buffer)

	for {
		p.Read(buffer)
		//fmt.Printf("%d\n", buffer)
	}
}
