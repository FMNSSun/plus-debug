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

	ctx := &pdbg.CryptoContext {
			Key : []byte{0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xA, 0xB, 0xC, 0xD, 0xE, 0xF},
			Secret: []byte{0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xA, 0xB, 0xC, 0xD, 0xE, 0xF},
		}

	connection.SetCryptoContext(ctx)

	go connectionManager.Listen()

	p := pdbg.NewPDbg(connection)

	buffer := make([]byte, 1024)

	p.Write(buffer)

	for {
		p.Read(buffer)
		//fmt.Printf("%d\n", buffer)
	}
}
