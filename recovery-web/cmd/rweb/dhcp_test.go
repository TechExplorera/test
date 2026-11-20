package main

import (
	"encoding/binary"
	"net"
	"testing"
)

func discover(t byte) []byte {
	p := make([]byte, 240)
	p[0], p[1], p[2] = 1, 1, 6
	copy(p[4:8], []byte{1, 2, 3, 4})
	copy(p[28:34], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	binary.BigEndian.PutUint32(p[236:240], 0x63825363)
	return append(p, 53, 1, t, 255)
}

func TestDHCP(t *testing.T) {
	srv, cli, mask := net.IP{192, 168, 90, 1}, net.IP{192, 168, 90, 2}, net.IP{255, 255, 255, 0}
	for in, want := range map[byte]byte{1: 2, 3: 5} {
		r := dhcpReply(discover(in), srv, cli, mask)
		if r == nil || r[0] != 2 || !net.IP(r[16:20]).Equal(cli) || r[242] != want {
			t.Fatalf("tipo %d: respuesta incorrecta %v", in, r)
		}
		if string(r[4:8]) != "\x01\x02\x03\x04" || r[28] != 0xaa {
			t.Fatal("xid/chaddr no copiados")
		}
	}
	if dhcpReply(discover(7), srv, cli, mask) != nil {
		t.Fatal("RELEASE no debe responderse")
	}
	if dhcpReply([]byte{1, 2, 3}, srv, cli, mask) != nil {
		t.Fatal("paquete corto aceptado")
	}
}
