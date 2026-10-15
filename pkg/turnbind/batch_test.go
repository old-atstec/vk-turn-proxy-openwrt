package turnbind

// A receive hands wireguard-go every packet already queued, up to the batch,
// in queue order — the first one waited for, the rest taken without waiting
// (proxy/rxbatch.go). Sabotage seen red: the drain loop removed (one packet
// per call); the drain bounded by 1 instead of len(packets); a waiting call
// for the rest (the receive would hang on an empty queue).

import (
	"net"
	"sync"
	"testing"

	"golang.zx2c4.com/wireguard/conn"
)

type fakeQueue struct {
	mu   sync.Mutex
	pkts [][]byte
}

func (q *fakeQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pkts) == 0 {
		return nil, false
	}
	p := q.pkts[0]
	q.pkts = q.pkts[1:]
	return p, true
}

func (q *fakeQueue) ReceivePacketUntil(done <-chan struct{}, buf []byte) (int, error) {
	select {
	case <-done:
		return 0, net.ErrClosed
	default:
	}
	p, ok := q.pop()
	if !ok {
		<-done // the test never lets the first packet wait: it would hang here
		return 0, net.ErrClosed
	}
	return copy(buf, p), nil
}

func (q *fakeQueue) TryReceivePacket(buf []byte) (int, bool) {
	p, ok := q.pop()
	if !ok {
		return 0, false
	}
	return copy(buf, p), true
}

func receiveOnce(t *testing.T, queued int) (int, [][]byte, []int, []conn.Endpoint) {
	t.Helper()
	q := &fakeQueue{}
	for i := 0; i < queued; i++ {
		q.pkts = append(q.pkts, []byte{byte(i), byte(i >> 8), 0xAA})
	}
	b := &TURNBind{src: q}
	fns, _, err := b.open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	batch := b.BatchSize()
	packets, sizes, eps := make([][]byte, batch), make([]int, batch), make([]conn.Endpoint, batch)
	for i := range packets {
		packets[i] = make([]byte, 64)
	}
	n, err := fns[0](packets, sizes, eps)
	if err != nil {
		t.Fatal(err)
	}
	return n, packets, sizes, eps
}

func TestReceiveHandsOverEveryQueuedPacketInOrder(t *testing.T) {
	n, packets, sizes, eps := receiveOnce(t, 5)
	if n != 5 {
		t.Fatalf("one receive returned %d packets of 5 queued", n)
	}
	for i := 0; i < n; i++ {
		if sizes[i] != 3 || packets[i][0] != byte(i) || eps[i] == nil {
			t.Errorf("packet %d: size %d first byte %d endpoint %v", i, sizes[i], packets[i][0], eps[i])
		}
	}
}

func TestReceiveStopsAtTheBatch(t *testing.T) {
	if b := (&TURNBind{}).BatchSize(); b != conn.IdealBatchSize {
		t.Fatalf("BatchSize %d, want wireguard-go's %d", b, conn.IdealBatchSize)
	}
	n, packets, _, _ := receiveOnce(t, conn.IdealBatchSize+40)
	if n != conn.IdealBatchSize {
		t.Fatalf("returned %d, want the batch %d", n, conn.IdealBatchSize)
	}
	if last := int(packets[n-1][0]) | int(packets[n-1][1])<<8; last != n-1 {
		t.Errorf("last packet of the batch is #%d, want #%d", last, n-1)
	}
}
