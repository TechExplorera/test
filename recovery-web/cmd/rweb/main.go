// rweb: recovery sin pantalla controlado desde el navegador por USB (RNDIS).
// Se ejecuta como servicio "recovery" del init stock de Samsung.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	devIP   = "192.168.90.1"
	hostIP  = "192.168.90.2"
	netmask = "255.255.255.0"
	port    = 80
)

var (
	flagDev    = flag.Bool("dev", false, "modo desarrollo en PC: sin USB/red, raíz simulada")
	flagListen = flag.String("listen", "", "dirección de escucha (por defecto 192.168.90.1:80)")
	flagRoot   = flag.String("root", "/", "raíz del sistema de archivos (modo dev)")
)

func main() {
	flag.Parse()
	setupLog()
	log.Printf("rweb %s iniciando (pid %d, dev=%v)", version, os.Getpid(), *flagDev)

	// init reinicia el servicio si muere: evitar bucles rápidos.
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)

	// fastbootd lo gestiona rweb; OTA o borrados pedidos desde Android, el recovery stock.
	if handleBCB() {
		enterFastbootd()
		select {} // el USB queda para fastbootd; salir con "fastboot reboot recovery"
	}

	listen := *flagListen
	if !*flagDev {
		ifname, err := setupUSB()
		if err != nil {
			log.Printf("USB: %v", err)
		}
		if ifname != "" {
			if err := ifUp(ifname, devIP, netmask); err != nil {
				log.Printf("red %s: %v", ifname, err)
			}
			go func() {
				for {
					if err := serveDHCP(ifname, devIP, hostIP, netmask); err != nil {
						log.Printf("DHCP: %v (reintento en 3s)", err)
					}
					time.Sleep(3 * time.Second)
				}
			}()
		}
		if listen == "" {
			listen = fmt.Sprintf("%s:%d", devIP, port)
		}
	} else if listen == "" {
		listen = "127.0.0.1:8080"
	}

	for {
		err := serveHTTP(listen)
		log.Printf("HTTP %s: %v (reintento en 2s)", listen, err)
		if !*flagDev {
			// Si falla al vincular la IP, escuchar en todas las interfaces.
			listen = fmt.Sprintf(":%d", port)
		}
		time.Sleep(2 * time.Second)
	}
}

func setupLog() {
	var w []io.Writer
	w = append(w, os.Stderr)
	if !*flagDev {
		_ = os.MkdirAll("/tmp", 0755)
		if f, err := os.OpenFile("/tmp/rweb.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			w = append(w, f)
		}
		if k, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
			w = append(w, &prefixWriter{k, "rweb: "})
		}
	}
	log.SetOutput(io.MultiWriter(w...))
	log.SetFlags(log.Ltime)
}

type prefixWriter struct {
	w io.Writer
	p string
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	_, err := p.w.Write(append([]byte(p.p), b...))
	return len(b), err
}
