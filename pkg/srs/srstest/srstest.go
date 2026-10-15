// Package srstest writes small sing-box rule-sets for tests: one default
// rule of exact names and domain_suffix entries (version 2), the format of
// pkg/srs's package comment.
package srstest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sort"
	"unicode/utf8"
)

// Domains is a rule-set of one rule: the names exactly, the suffixes with
// their subdomains.
func Domains(exact, suffix []string) []byte {
	var keys []string
	for _, d := range exact {
		keys = append(keys, reverse(d))
	}
	for _, d := range suffix {
		keys = append(keys, reverse("\n"+d))
	}
	sort.Strings(keys)
	leaves, bitmap, labels := louds(keys)
	var b bytes.Buffer
	uv := func(v uint64) { b.Write(binary.AppendUvarint(nil, v)) }
	u64s := func(v []uint64) {
		uv(uint64(len(v)))
		for _, x := range v {
			b.Write(binary.BigEndian.AppendUint64(nil, x))
		}
	}
	uv(1)          // one rule
	b.WriteByte(0) // a default rule
	b.WriteByte(2) // domain
	b.WriteByte(0)
	u64s(leaves)
	u64s(bitmap)
	uv(uint64(len(labels)))
	b.Write(labels)
	b.WriteByte(0xFF) // final
	b.WriteByte(0)    // not inverted
	var out bytes.Buffer
	out.WriteString("SRS\x02")
	zw := zlib.NewWriter(&out)
	zw.Write(b.Bytes())
	zw.Close()
	return out.Bytes()
}

func louds(keys []string) (leaves, bitmap []uint64, labels []byte) {
	set := func(bm *[]uint64, i int, v bool) {
		for i>>6 >= len(*bm) {
			*bm = append(*bm, 0)
		}
		if v {
			(*bm)[i>>6] |= 1 << (i & 63)
		}
	}
	type span struct{ s, e, col int }
	q := []span{{0, len(keys), 0}}
	bit := 0
	for i := 0; i < len(q); i++ {
		sp := q[i]
		if sp.col == len(keys[sp.s]) {
			sp.s++
			set(&leaves, i, true)
		}
		for j := sp.s; j < sp.e; {
			from := j
			for j < sp.e && keys[j][sp.col] == keys[from][sp.col] {
				j++
			}
			q = append(q, span{from, j, sp.col + 1})
			labels = append(labels, keys[from][sp.col])
			set(&bitmap, bit, false)
			bit++
		}
		set(&bitmap, bit, true)
		bit++
	}
	return
}

func reverse(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		utf8.EncodeRune(b[len(s)-i-n:], r)
		i += n
	}
	return string(b)
}
