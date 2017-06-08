package main

import "github.com/FMNSSun/plus-debug/pdbg"
import "plus"
import "net"
import "fmt"
import "os"
import "time"

func main() {
	//PLUS.LoggerDestination = os.Stdout
	server(os.Args[1])
}

func server(laddr string) {
	packetConn, err := net.ListenPacket("udp", laddr)

	if err != nil {
		panic("Could not create packet connection!")
	}


	connectionManager := PLUS.NewConnectionManager(packetConn)

	connectionManager.SetInitConn(func(c *PLUS.Connection) error {
		ctx := &pdbg.CryptoContext {
			Key : []byte{0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xA, 0xB, 0xC, 0xD, 0xE, 0xF},
			Secret: []byte{0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xA, 0xB, 0xC, 0xD, 0xE, 0xF},
		}

		c.SetCryptoContext(ctx)

		return nil
	})

	go connectionManager.Listen()

	for {
		connection := connectionManager.Accept()
		fmt.Println("Got connection")

		go func() {
			p := pdbg.NewPDbg(connection)
			for i := 0; i < 4096; i++ {
				q := byte(i % 256)
				p.Write([]byte{q, 1, q, 2, q})
				time.Sleep(1 * time.Millisecond)
			}
			p.Flush()
			p.RequestClose()
		}()
	}
}
