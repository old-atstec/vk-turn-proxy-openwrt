package proxy

// TryReceivePacket takes what is queued and never waits; what it takes is
// counted like ReceivePacket's. Sabotage seen red: a blocking receive (the
// empty case hangs); the counters skipped.

import "testing"

func TestTryReceivePacketTakesTheQueuedAndNeverWaits(t *testing.T) {
	p := &Proxy{recvCh: make(chan []byte, 4)}
	buf := make([]byte, 16)
	if _, ok := p.TryReceivePacket(buf); ok {
		t.Fatal("an empty queue gave a packet")
	}
	pkt := recvPktPoolGet(3)
	copy(pkt, []byte{1, 2, 3})
	p.recvCh <- pkt
	n, ok := p.TryReceivePacket(buf)
	if !ok || n != 3 || buf[0] != 1 {
		t.Fatalf("got n=%d ok=%v buf=%v", n, ok, buf[:3])
	}
	if p.rxPackets.Load() != 1 || p.rxBytes.Load() != 3 {
		t.Errorf("counted %d packets %d bytes, want 1 and 3", p.rxPackets.Load(), p.rxBytes.Load())
	}
	if _, ok := p.TryReceivePacket(buf); ok {
		t.Fatal("the queue gave a second packet it did not hold")
	}
}
