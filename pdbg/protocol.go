package pdbg

import "github.com/mami-project/plus-lib"
import "fmt"
import "sync"
import "encoding/binary"
import "sync/atomic"
import "time"
import "errors"
import "math/rand"

const windowSize uint32 = 2048
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
	feedbackChan chan []byte
	sendWindowNumber uint32
	recvWindowNumber uint32
	nextPacketNoSeq uint32
	requestClose uint32
	byeSent bool
	byeChan chan bool
	closed uint32
}

type feedbackChannel struct {
	feedbackChan chan []byte
	p *PDbg
}

func (ch *feedbackChannel) SendFeedback(data []byte) error {
	ch.p.log("Need to send back feedback data %d", data)
	select {
		case ch.feedbackChan <- data:
		default:
	}
	return nil
}

func (p *PDbg) log(message string, a ...interface{}) {
	mutex.Lock()
	defer mutex.Unlock()

	fmt.Printf("%d\t%s\t\t%s\t\t%s\n",
		time.Now().UnixNano() / int64(1000000),
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
	p.ackWait = make(chan uint32, 2)
	p.feedbackChan = make(chan []byte, 8)
	p.byeChan = make(chan bool, 1)
	p.sendWindowNumber = 0
	p.recvWindowNumber = 0
	p.nextPacketNoSeq = 0
	p.closed = 0
	p.requestClose = 0
	p.byeSent = false

	conn.SetFeedbackChannel(&feedbackChannel { feedbackChan : p.feedbackChan, p : &p })

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

func (p *PDbg) sendHelo(curWindow uint32) {
	heloBuffer := []byte{0x88, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(heloBuffer[1:], curWindow)
	p.conn.Write(heloBuffer)

	p.log("Sent HELO.")
}

func (p *PDbg) sendBye(curWindow uint32) {
	byeBuffer := []byte{0x77, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(byeBuffer[1:], curWindow)
	p.conn.Write(byeBuffer)

	p.log("Sent BYE.")
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

func (p *PDbg) ioReader(ch chan []byte) {
	for {
		buf := make([]byte, 4096)
		n, err := p.conn.Read(buf)
		
		if err != nil {
			p.log("ioReader: %s", err.Error())
			return
		}

		if rand.Int() % 100 != 0 {
			ch <- buf[:n]
		}
	}
}

func (p *PDbg) Close() {
	p.log("Closing!")

	closed := atomic.LoadUint32(&p.closed)

	if closed != 0 {
		p.log("Already closed!")
		return //already closed
	}

	p.log("Notifying readers about close.")
	select {
		case p.readChan <- nil:
		default:
	}
	p.log("Notified readers about close.")
	p.log("Notifying writer about close.")
	select {
		case p.writeChan <- nil:
		default:
	}
	p.log("Notified writer about close.")
	p.log("Closing conn...")
	p.conn.Close()
	p.log("Closed conn!")

	atomic.AddUint32(&p.closed, 1) // set closed to 1
}

func (p *PDbg) Flush() {
	for {
		if !p.HavePacketsOutstanding() {
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
}

func (p *PDbg) readLoop() {
	p.log("readloop")

	timer := time.NewTimer(2000 * time.Millisecond)

	ch := make(chan []byte, 8)

	go p.ioReader(ch)

	for {
		var buf []byte

		select {
		case _ = <- timer.C:
			p.log("Receive timeout occured!")
			p.Close()
			return
		case buf = <- ch:
			timer.Reset(2000 * time.Millisecond)
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

		case 0xAA:

			p.log("PCF_FBCK packet received: %d", buf[5:])

			p.conn.AddFeedbackData(buf[5:])

		case 0x88:

			p.log("HELO packet received!")

		case 0x77:

			p.log("BYE packet received!")

			select {
				case p.byeChan <- true:
				default:
			}

			p.bye()

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
				p.log("DATA packet is duplicate: %d / %d", windowNumber, packetNo)
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
					} else {
						break
					}

					select {
						case p.readChan <- buf[8:]: // forward it to reader
						default:
					}
				}

				p.log("Forward scanned to %d", p.nextPacketNoSeq)
			}
		}
	}
}

func (p *PDbg) waitAck(curWindow uint32, windowSize uint32) bool {
	p.log("Wait for ACK %d window size %d", curWindow, windowSize)

	p.requestAck(curWindow, windowSize)

	select {
		case ackedWindow := <- p.ackWait:
			if ackedWindow == curWindow {
				goto exitAck // wait for this window to be acked
			}
		default:
			return false
	}

	exitAck:

	p.log("ACK received!")
	
	for i := uint32(0); i < windowSize; i++ {
		p.outPackets[i] = nil
	}

	atomic.AddUint32(&p.sendWindowNumber, 1)

	if atomic.LoadUint32(&p.requestClose) != 0 {
		// Close was requested so...
		p.log("Close was requested and last window was acked so let's bye this.")
		if p.HavePacketsOutstanding() {
			p.log("Have outstanding packets. Can't bye yet tho!")
		} else {
			p.sendBye(curWindow)
		}
	}

	return true
}

func (p *PDbg) HavePacketsOutstanding() bool {
	if len(p.writeChan) > 0 || len(p.resendChan) > 0 {
		return true
	} else {
		return false
	}
}

func (p *PDbg) bye() {
	p.log("Bye")
	curWindow := atomic.LoadUint32(&p.sendWindowNumber)
	p.sendBye(curWindow)
	p.byeSent = true
	go func() {
		time.Sleep(1000 * time.Millisecond)
		p.log("Bye timeout")
		select {
			case p.byeChan <- true:
			default:
		}
	}()
}

func (p *PDbg) IsClosed() bool {
	closed := atomic.LoadUint32(&p.closed)

	return closed != 0		
}

func (p *PDbg) IsCloseRequested() bool {
	return atomic.LoadUint32(&p.requestClose) != 0
}

func (p *PDbg) RequestClose() {
	if atomic.LoadUint32(&p.requestClose) != 0 {
		p.log("Close was already requested!")
		return
	} else {
		p.log("Request close!")
	}

	atomic.StoreUint32(&p.requestClose, 1)
}

func (p *PDbg) writeLoop() {
	p.log("writeloop")

	packetNo := uint32(0)

	timer := time.NewTimer(1000 * time.Millisecond)
	heloTimer := time.NewTimer(1000 * time.Millisecond)

	writeChan := p.writeChan

	for {
		if p.IsClosed() {
			p.log("writeloop: closed.")
			return
		}

		select {
		case _ = <- p.byeChan:
			p.log("Close negotiated!")
			if p.byeSent {
				p.Close()
			}

		case _ = <- heloTimer.C:			
			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			p.sendHelo(curWindow)

			heloTimer.Reset(1000 * time.Millisecond)

		case _ = <- timer.C:
			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			if packetNo == 0 {
				p.log("Send timeout but no packet was sent anyway!")

				if p.IsCloseRequested() {
					p.sendBye(curWindow)
				}

				continue
			}

			// timeout
			p.log("Send timeout occured")

			writeChan = nil
			if p.waitAck(curWindow, packetNo) {
				packetNo = 0
				writeChan = p.writeChan
			}

			timer.Reset(10 * time.Millisecond)
			

		case toResend := <- p.resendChan:
			data := p.outPackets[toResend]

			_, err := p.conn.Write(data)

			p.log("Retransmission")

			if err != nil {
				panic("oops!")
			}

			timer.Reset(10 * time.Millisecond)

		case feedback := <- p.feedbackChan:
			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			buffer := make([]byte, 1 + 4 + len(feedback))

			copy(buffer[5:], feedback)
			buffer[0] = 0xAA
			binary.LittleEndian.PutUint32(buffer[1:], curWindow)

			p.conn.Write(buffer)

			p.log("Sent PCF_FBCK packet!")

		case toSend := <- writeChan:
			if toSend == nil {
				// Close requested
				p.log("Stopping writer!")
				return
			}

			curWindow := atomic.LoadUint32(&p.sendWindowNumber)

			buffer := make([]byte, 96 + len(toSend))
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
				writeChan = nil // don't write when waiting for an ACK.
				if p.waitAck(curWindow, windowSize) {
					packetNo = 0
					writeChan = p.writeChan
				}
				timer.Reset(10 * time.Millisecond)
				continue
			}
			timer.Reset(1000 * time.Millisecond)
		}
	}
}

func (p *PDbg) Read(data []byte) (int, error) {
	if p.IsClosed() {
		return 0, errors.New("PDbg closed!")
	}

	data_ := <- p.readChan

	if data_ == nil {
		p.log("Read from closed!")
		return 0, errors.New("PDbg closed!")
	}

	n := copy(data, data_)
	return n, nil
}

func (p *PDbg) Write(data []byte) (int, error) {
	if p.IsClosed() {
		return 0, errors.New("PDbg closed!")
	}

	if len(data) > maxPacketSize - 96 { //we need some space for protocol headers
		panic("oops!")
	}

	data_ := make([]byte, len(data))
	copy(data_, data)
	p.writeChan <- data_
	return len(data), nil
}
