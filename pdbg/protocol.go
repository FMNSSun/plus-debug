package pdbg

import "plus"
import "fmt"
import "sync"
import "encoding/binary"
import "sync/atomic"
import "time"

const windowSize uint32 = 2
const maxPacketSize = 4096

var mutex = &sync.Mutex{}



type PDbg struct {
	inPackets [windowSize][]byte
	outPackets [windowSize][]byte
	ackWait chan uint32
	conn *PLUS.Connection	
	readChan chan []byte
	writeChan chan []byte
	resendChan chan uint32
	mutex *sync.Mutex
	sendWindowNumber uint32
	recvWindowNumber uint32
	nextPacketNoSeq uint32
}

func (p *PDbg) log(message string, a ...interface{}) {
	mutex.Lock()
	defer mutex.Unlock()

	fmt.Printf("%s\t\t%s\t\t%s\n",
		p.conn.LocalAddr().String(),
		p.conn.RemoteAddr().String(),
		fmt.Sprintf(message, a...))
}

func NewPDbg(conn *PLUS.Connection) *PDbg {
	var p PDbg
	p.conn = conn
	p.readChan = make(chan []byte, 8)
	p.writeChan = make(chan []byte, 8)
	p.resendChan = make(chan uint32, 32)
	p.ackWait = make(chan uint32, 1)
	p.sendWindowNumber = 0
	p.recvWindowNumber = 0
	p.nextPacketNoSeq = 0

	go p.readLoop()
	go p.writeLoop()

	p.log("new")

	return &p
}

func (p *PDbg) requestAck(curWindow uint32, windowSize uint32) {
	reqBuffer := []byte{0xCC, 0x00, 0x00, 0x00, 0x00,
							  0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(reqBuffer[1:], curWindow)
	binary.LittleEndian.PutUint32(reqBuffer[5:], windowSize)
	p.conn.Write(reqBuffer)

	p.log("Sent REQ_ACK: %d window size %d", curWindow, windowSize)
}

func (p *PDbg) sendAck(curWindow uint32) {
	ackBuffer := []byte{0xFF, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(ackBuffer[1:], curWindow)
	p.conn.Write(ackBuffer)

	p.log("Sent ACK: %d", curWindow)
}

func (p *PDbg) requestResend(packetNo uint32, curWindow uint32) {
	resendBuffer := []byte{0xBB, 0x00, 0x00, 0x00, 0x00,
						         0x00, 0x00, 0x00, 0x00}

	binary.LittleEndian.PutUint32(resendBuffer[1:], curWindow)
	binary.LittleEndian.PutUint32(resendBuffer[5:], packetNo)
	p.conn.Write(resendBuffer)

	p.log("Sent REQ_RESEND: %d/%d", packetNo, curWindow)
}

func (p *PDbg) readLoop() {
	p.log("readloop")

	for {
		buf := make([]byte, 4096)
		_, err := p.conn.Read(buf)

		if err != nil {
			panic("oops!")
		}

		curWindow := p.recvWindowNumber
		curSendWindow := atomic.LoadUint32(&p.sendWindowNumber)
		windowNumber := uint32(binary.LittleEndian.Uint32(buf[1:]))


		switch buf[0] {
		case 0xFF: 
			// It's an ack window packet
			p.log("ACK packet received for %d", windowNumber)

			if windowNumber != curSendWindow {
				// fishy. A resend for a window in the future/past?
				p.log("ACK packet has window number %d but expected %d", windowNumber, curSendWindow)
				continue // ignore it
			}

			select {
				case p.ackWait <- windowNumber: //forward it
				default:
					// well, drop the ack packet
			}

		case 0xBB:
			// It's a request for resend

			packetNoToResend := binary.LittleEndian.Uint32(buf[5:])

			p.log("REQ_RESEND packet received for %d/%d", windowNumber, packetNoToResend)

			if windowNumber != curSendWindow {
				// fishy. A resend for a window in the future/past?
				p.log("REQ_RESEND packet has window number %d but expected %d", windowNumber, curSendWindow)
				continue // ignore it
			}

			select {
				case p.resendChan <-packetNoToResend: //forward resend request to writer
					p.log("REQ_RESEND packet forwarded to writer")
				default:
			}

		case 0xCC:
			// It's a request for ack

			
			windowSize_ := binary.LittleEndian.Uint32(buf[5:])

			p.log("REQ_ACK packet received for %d highest packet %d", windowNumber, windowSize_)


			if windowNumber > curWindow {
				// fishy. A requset for ack for a window in the future/past?
				p.log("REQ_ACK window packet has window number %d > %d", windowNumber, curWindow)
				continue // ignore it
			} else if windowNumber < curWindow {
				p.log("REQ_ACK window packet for past window received. Re-acking... %d", windowNumber)
				// We already acked this so uhm... re-ack it. 
				p.sendAck(windowNumber) // don't ack currentWindow, ack the past window!
				continue
			}

			if p.nextPacketNoSeq == windowSize_ {
				// Ideal case, we have received all packets
				p.log("Received all packets!")

				p.sendAck(curWindow)
				p.recvWindowNumber++ // expect the next window to be sent

				p.log("Clearing inpackets")
				for i := uint32(0); i < windowSize; i++ {
					p.inPackets[i] = nil
				}

				p.log("Await packet of next window")
				p.nextPacketNoSeq = 0
			} else {
				// Let's see what we need to request
				for i := uint32(0); i < windowSize_; i++ {
					if p.inPackets[i] == nil {
						p.requestResend(i, curWindow)
					}
				}
			}
				

		case 0x00:
			// It's a packet

			packetNo := binary.LittleEndian.Uint32(buf[5:])

			p.log("DATA packet received for %d/%d", windowNumber, packetNo)

			if windowNumber != curWindow {
				// fishy. A packet for a window in the future/past?
				p.log("DATA packet has window number %d but expected %d", windowNumber, curWindow)
				continue // ignore it
			}

			if p.inPackets[packetNo] != nil {
				// duplicate packet?
			} else {
				p.inPackets[packetNo] = buf[8:]
			}

			if packetNo == p.nextPacketNoSeq { // is this the next in sequence packet?
				p.nextPacketNoSeq++

				select {
					case p.readChan <- buf[8:]: // forward it to reader
					default:
				}

				// forward scan
				// because we might have already received packets that are now in
				// sequence too. (i.e. we receveied 1,2,4,5,6,7,8,9 and nextPacketNoSeq
				// was 3 and the packet 3 arrives (retranmsission, out of order) we
				// all packets 4,5,6,7,8,9 are now in order too for example).
				for i := p.nextPacketNoSeq; i < windowSize; i++ {
					if p.inPackets[i] != nil {
						p.nextPacketNoSeq++
					}

					select {
						case p.readChan <- buf[8:]: // forward it to reader
						default:
					}
				}
			}
		}
	}
}

func (p *PDbg) waitAck(curWindow uint32, windowSize uint32) {
	p.log("Wait for ACK %d window size %d", curWindow, windowSize)

	p.requestAck(curWindow, windowSize)

	time.Sleep(10 * time.Millisecond)

	select {
		case ackedWindow := <- p.ackWait:
			if ackedWindow == curWindow {
				goto exitAck // wait for this window to be acked
			}
		default:
			return
	}

	exitAck:

	p.log("ACK received!")
	
	for i := uint32(0); i < windowSize; i++ {
		p.outPackets[i] = nil
	}

	atomic.AddUint32(&p.sendWindowNumber, 1)
}

func (p *PDbg) writeLoop() {
	p.log("writeloop")

	packetNo := uint32(0)

	timer := time.NewTimer(1000 * time.Millisecond)

	for {
		select {
		case _ = <- timer.C:
			if packetNo == 0 {
				p.log("Send timeout but no packet was sent anyway!")
				continue
			}

			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			// timeout
			p.log("Send timeout occured")

			p.waitAck(curWindow, packetNo)
			packetNo = 0
			timer.Reset(1000 * time.Millisecond)
			

		case toResend := <- p.resendChan:
			p.log("HTNH")

			data := p.outPackets[toResend]

			_, err := p.conn.Write(data)

			p.log("Retransmission")

			if err != nil {
				panic("oops!")
			}

			timer.Reset(1000 * time.Millisecond)		

		case toSend := <- p.writeChan:
			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			buffer := make([]byte, maxPacketSize)
			copy(buffer[96:], toSend)
	
			buffer[0] = 0x00
			binary.LittleEndian.PutUint32(buffer[1:], curWindow)
			binary.LittleEndian.PutUint32(buffer[5:], packetNo)

			_, err := p.conn.Write(buffer)

			if err != nil {
				panic("oops!")
			}

			p.log("Sent packet %d/%d", curWindow, packetNo)

			p.outPackets[packetNo] = buffer

			packetNo++

			if packetNo == windowSize {

				p.waitAck(curWindow, windowSize)

				packetNo = 0
			}

			timer.Reset(1000 * time.Millisecond)
		}
	}
}

func (p *PDbg) Read(data []byte) (int, error) {
	data_ := <- p.readChan
	n := copy(data, data_)
	return n, nil
}

func (p *PDbg) Write(data []byte) (int, error) {
	if len(data) > maxPacketSize - 96 { //we need some space for protocol headers
		panic("oops!")
	}

	data_ := make([]byte, len(data))
	copy(data_, data)
	p.writeChan <- data_
	return len(data), nil
}
