package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"syscall"
	"time"
	"unsafe"
)

const (
	siocgifflags   = 0x8913
	siocsifflags   = 0x8914
	siocsifaddr    = 0x8916
	siocsifnetmask = 0x891c
)

type ifreqAddr struct {
	Name [16]byte
	Addr syscall.RawSockaddrInet4
	_    [8]byte
}

type ifreqFlags struct {
	Name  [16]byte
	Flags uint16
	_     [22]byte
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func setAddr(fd int, name string, req uintptr, ip string) error {
	var r ifreqAddr
	copy(r.Name[:], name)
	r.Addr.Family = syscall.AF_INET
	copy(r.Addr.Addr[:], net.ParseIP(ip).To4())
	return ioctl(fd, req, unsafe.Pointer(&r))
}

// ifUp asigna IP/máscara y levanta la interfaz.
func ifUp(name, ip, mask string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	var f ifreqFlags
	copy(f.Name[:], name)
	if err := ioctl(fd, siocgifflags, unsafe.Pointer(&f)); err != nil {
		return fmt.Errorf("leer flags: %w", err)
	}
	f.Flags |= syscall.IFF_UP | syscall.IFF_RUNNING
	if err := ioctl(fd, siocsifflags, unsafe.Pointer(&f)); err != nil {
		return fmt.Errorf("levantar: %w", err)
	}
	if err := setAddr(fd, name, siocsifaddr, ip); err != nil {
		return fmt.Errorf("asignar IP: %w", err)
	}
	if err := setAddr(fd, name, siocsifnetmask, mask); err != nil {
		return fmt.Errorf("asignar máscara: %w", err)
	}
	log.Printf("red %s = %s/%s", name, ip, mask)
	return nil
}

// serveDHCP: servidor DHCP mínimo que siempre entrega hostIP al PC.
// No anuncia puerta de enlace ni DNS para no robar la conexión a internet del PC.
func serveDHCP(ifname, srv, client, mask string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	if err := syscall.BindToDevice(fd, ifname); err != nil {
		return fmt.Errorf("bind a %s: %w", ifname, err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: 67}); err != nil {
		return fmt.Errorf("bind :67: %w", err)
	}
	log.Printf("DHCP escuchando en %s", ifname)

	srvIP := net.ParseIP(srv).To4()
	cliIP := net.ParseIP(client).To4()
	maskIP := net.ParseIP(mask).To4()
	buf := make([]byte, 1500)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return err
		}
		resp := dhcpReply(buf[:n], srvIP, cliIP, maskIP)
		if resp == nil {
			continue
		}
		dst := &syscall.SockaddrInet4{Port: 68, Addr: [4]byte{255, 255, 255, 255}}
		if err := syscall.Sendto(fd, resp, 0, dst); err != nil {
			log.Printf("DHCP envío: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func dhcpReply(p []byte, srv, cli, mask net.IP) []byte {
	if len(p) < 240 || p[0] != 1 || binary.BigEndian.Uint32(p[236:240]) != 0x63825363 {
		return nil
	}
	var msgType byte
	for i := 240; i < len(p); {
		code := p[i]
		if code == 255 {
			break
		}
		if code == 0 {
			i++
			continue
		}
		if i+1 >= len(p) {
			break
		}
		l := int(p[i+1])
		if code == 53 && l == 1 && i+2 < len(p) {
			msgType = p[i+2]
		}
		i += 2 + l
	}
	var reply byte
	switch msgType {
	case 1: // DISCOVER
		reply = 2 // OFFER
	case 3: // REQUEST
		reply = 5 // ACK
	default:
		return nil
	}
	r := make([]byte, 240, 300)
	r[0] = 2 // BOOTREPLY
	r[1], r[2] = p[1], p[2]
	copy(r[4:8], p[4:8])     // xid
	copy(r[10:12], p[10:12]) // flags
	copy(r[16:20], cli)      // yiaddr
	copy(r[20:24], srv)      // siaddr
	copy(r[28:44], p[28:44]) // chaddr
	binary.BigEndian.PutUint32(r[236:240], 0x63825363)
	lease := []byte{0, 1, 0x51, 0x80} // 86400 s
	r = append(r, 53, 1, reply)
	r = append(r, 54, 4)
	r = append(r, srv...)
	r = append(r, 51, 4)
	r = append(r, lease...)
	r = append(r, 1, 4)
	r = append(r, mask...)
	r = append(r, 255)
	return r
}
