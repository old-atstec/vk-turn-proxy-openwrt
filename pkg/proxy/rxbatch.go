package proxy

// Batched receive: a ReceiveFunc that hands wireguard-go several packets per
// call (turnbind) takes the first with ReceivePacketUntil and the rest, those
// already queued, with TryReceivePacket. wireguard-go's receive path is per
// batch — one wake-up of the peer's sequential receiver and one tun.Write
// call, in which the tun's GRO (vnet header, Linux) coalesces in-order TCP
// segments into one write. Fed one packet per call, as before, every packet
// was its own wake-up and its own write(2): on the router 11 % of the
// daemon's CPU sat in the tun's write alone (profile 2026-10-15).

// TryReceivePacket is ReceivePacket without the wait: a packet already queued,
// or ok false at once.
func (p *Proxy) TryReceivePacket(buf []byte) (n int, ok bool) {
	select {
	case pkt := <-p.recvCh:
		return p.deliverRx(pkt, buf), true
	default:
		return 0, false
	}
}

// deliverRx copies a dequeued packet into buf, returns it to its pool and
// counts it.
//
// Downlink disorder is measured HERE and not at the producer side of recvCh.
// 🚨 That is not a detail: enqueueRecv is entered by up to 30 goroutines that
// then race independently for the channel, so the order observed there is the
// order they ARRIVED, not the order they won — measured at ~10x less disorder
// than the queue actually delivers, and always in the direction that would
// have produced the false reading "the downlink arrives near-ordered". The
// consumers (ReceivePacketUntil, TryReceivePacket) are called from one
// conn.ReceiveFunc ⇒ one RoutineReceiveIncoming, in order, so this is by
// construction the order wireguard-go gets. → rxreorder.go
func (p *Proxy) deliverRx(pkt, buf []byte) int {
	downlinkReorder.observeRx(pkt)
	n := copy(buf, pkt)
	// pkt was allocated via recvPktPoolGet by the producer recv goroutine —
	// return to pool now that contents are copied out. Saves the per-packet
	// allocation that previously hit GC. Build 133.
	recvPktPoolPut(pkt)
	p.rxBytes.Add(int64(n))
	p.rxPackets.Add(1)
	return n
}
