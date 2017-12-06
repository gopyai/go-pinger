package pinger

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	protocolICMP = 1
)

type (
	PingResult struct {
		Times          int
		ReplyRatio     float64
		LatencyAverage float64
	}

	bySeq struct {
		ip      net.IP
		ipIdx   int       // set when start echo
		echoAt  time.Time //
		latency float64   // updated at each reply
	}

	byIP struct {
		times      int     // updated at each reply
		latencySum float64 //
	}
)

var (
	ErrParseReply   = errors.New("error: Parse ICMP message expect ipv4.ICMPTypeEchoReply")
	ErrDisconnected = errors.New("error: No active connection")
	ErrSendEcho     = errors.New("error: Failed to send ICMP echo message")

	conn struct {
		sync.Mutex
		*icmp.PacketConn
		onReply map[int]func(seq int) // Map seq to callback function
		seq     int
	}
)

func Start() error {
	syncConnDestroy()
	return syncConnCreate()
}

func Stop() {
	syncConnDestroy()
}

func bgReply() error {
	//if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
	//	stop()
	//	return err
	//}

	for {
		if seq, e := syncReplyRead(); e == nil {
			// Notes:
			// So far the only identified error is the "destination unreachable".
			// So it is safe to ignore the error.
			syncReplyCall(seq)
		}
	}
}

func Ping(pingList []net.IP, times int, durBetween, durLast time.Duration) ([]*PingResult, error) {
	rst := make([]*PingResult, len(pingList))
	rstByIP := make([]*byIP, len(pingList))
	rstBySeq := make(map[int]*bySeq)

	for ipIdx := 0; ipIdx < len(pingList); ipIdx++ {
		rst[ipIdx] = new(PingResult)
		rstByIP[ipIdx] = new(byIP)
	}

	done := make(chan bool, 1)

	for loop := 0; loop < times; loop++ {
		for ipIdx, ip := range pingList {
			err := syncPingEcho(ip,
				func(seq int, ip net.IP) { // On echo
					rstBySeq[seq] = &bySeq{ip: ip, ipIdx: ipIdx, echoAt: time.Now()}
				},
				func(seq int) { // On reply
					rs, ok := rstBySeq[seq]
					if !ok {
						return
					}
					//fmt.Println("Reply:", rs.ip)
					rs.latency = time.Since(rs.echoAt).Seconds()

					rip := rstByIP[rs.ipIdx]
					rip.times++
					rip.latencySum += rs.latency

					delete(rstBySeq, seq)
					if len(rstBySeq) == 0 {
						select {
						case done <- true:
						default:
							return
						}
					}
				})
			if err != nil {
				return nil, err
			}
		}
		time.Sleep(durBetween)
	}

	go func() {
		time.Sleep(durLast)
		syncPingClearSeq(rstBySeq)
		done <- true
	}()

	<-done
	return syncPingSummarize(rst, rstByIP, times)
}

func syncConnCreate() error {
	conn.Lock()
	defer conn.Unlock()
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return err
	}

	conn.PacketConn = c
	conn.onReply = make(map[int]func(int))
	go bgReply()

	return nil
}

func syncConnDestroy() {
	conn.Lock()
	defer conn.Unlock()
	if conn.PacketConn == nil {
		return // Already disconnected
	}
	conn.Close()

	conn.PacketConn = nil
	conn.onReply = nil
}

func syncReplyRead() (seq int, err error) {
	conn.Lock()
	c := conn.PacketConn
	if c == nil {
		conn.Unlock()
		return 0, ErrDisconnected
	}
	conn.Unlock() // no need to defer because read should not concurrent

	readBuffer := make([]byte, 1500)
	n, peer, err := c.ReadFrom(readBuffer)
	if err != nil {
		//switch err.(type) {
		//case *net.OpError:
		//	// Timeout error may happen
		//	if strings.Contains(err.Error(), "i/o timeout") {
		//		return ErrReadTimeout
		//	}
		//default:
		//}
		return 0, err
	}

	msg, err := icmp.ParseMessage(protocolICMP, readBuffer[:n])
	if err != nil {
		return 0, err
	}

	switch msg.Type {
	case ipv4.ICMPTypeEchoReply:
	default:
		// Notes:
		// So far the only identified error is the "destination unreachable".
		// So it is safe to ignore the error.

		//fmt.Println("error: Got", msg, "from", peer)
		_ = peer
		return 0, ErrParseReply
	}

	b, err := msg.Body.Marshal(protocolICMP)
	if err != nil {
		return 0, err
	}

	return int(binary.BigEndian.Uint16(b[2:4])), nil
}

func syncReplyCall(seq int) {
	conn.Lock()
	defer conn.Unlock()
	if onReply, ok := conn.onReply[seq]; ok {
		onReply(seq)
		delete(conn.onReply, seq)
	}
}

func syncPingEcho(ip net.IP, onEcho func(seq int, ip net.IP), onReply func(seq int)) error {
	conn.Lock()
	defer conn.Unlock()

	onEcho(conn.seq, ip)
	conn.onReply[conn.seq] = onReply

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{
			ID:   os.Getpid() & 0xffff, Seq: conn.seq,
			Data: []byte("HELLO-R-U-THERE"),
		},
	}
	conn.seq++

	b, err := msg.Marshal(nil)
	if err != nil {
		return err
	}

	n, err := conn.WriteTo(b, &net.IPAddr{IP: ip})
	if err != nil {
		return err
	}
	if n != len(b) {
		return ErrSendEcho
	}

	return nil
}

func syncPingClearSeq(rstBySeq map[int]*bySeq) {
	conn.Lock()
	defer conn.Unlock()
	for seq := range rstBySeq {
		delete(rstBySeq, seq)
	}
}

func syncPingSummarize(rst []*PingResult, rstByIP []*byIP, times int) ([]*PingResult, error) {
	conn.Lock()
	defer conn.Unlock()
	for ipIdx, r := range rst {
		rip := rstByIP[ipIdx]
		if rip.times == 0 {
			continue
		}
		r.Times = rip.times
		r.ReplyRatio = float64(rip.times) / float64(times)
		r.LatencyAverage = rip.latencySum / float64(rip.times)
	}
	return rst, nil
}
