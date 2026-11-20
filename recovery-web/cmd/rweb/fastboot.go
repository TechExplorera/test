package main

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Modo fastbootd: se arranca el fastbootd del ramdisk stock directamente, sin pasar
// por el recovery de Samsung (que solo lo permite con un "fastbootd token" en
// /proc/cmdline). Las reglas de init.rc stock hacen el resto:
//   sys.usb.config=fastboot              -> start fastbootd
//   fastbootd escribe descriptores       -> sys.usb.ffs.ready=1
//   config=fastboot && ffs.ready=1       -> enlaza ffs.fastboot y activa el UDC
// El panel web deja de estar accesible (el USB pasa a ser fastboot).
// Para volver: "fastboot reboot recovery".

var fastbootOnce sync.Once

func setprop(k, v string) error {
	out, err := exec.Command("/system/bin/setprop", k, v).CombinedOutput()
	if err != nil {
		log.Printf("setprop %s=%s: %v %s", k, v, err, strings.TrimSpace(string(out)))
	}
	return err
}

// releaseRNDIS deshace la configuración USB de rweb y devuelve el gadget al estado
// que esperan las reglas de init (sin funciones enlazadas, VID de Google).
func releaseRNDIS() {
	_ = wr(gadget+"/UDC", "\n")
	cfg := gadget + "/configs/b.1"
	if ents, err := os.ReadDir(cfg); err == nil {
		for _, e := range ents {
			p := cfg + "/" + e.Name()
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(p)
			}
		}
	}
	_ = os.Remove(gadget + "/os_desc/b.1")
	_ = wr(gadget+"/os_desc/use", "0")
	_ = wr(gadget+"/idVendor", "0x18D1") // Google: driver estándar de fastboot
	if m := getprop("ro.product.model"); m != "" {
		_ = wr(gadget+"/strings/0x409/product", m)
	}
}

func enterFastbootd() {
	fastbootOnce.Do(func() {
		log.Print("entrando en modo fastbootd")
		if err := clearBCB(); err != nil {
			log.Printf("limpiar BCB: %v", err)
		}
		if *flagDev {
			return
		}
		releaseRNDIS()
		_ = setprop("sys.usb.config", "none")
		time.Sleep(500 * time.Millisecond)
		if err := setprop("sys.usb.config", "fastboot"); err != nil {
			// Respaldo si la propiedad no dispara la regla de init.
			_ = setprop("ctl.start", "fastbootd")
		}
		for i := 0; i < 40; i++ {
			if getprop("sys.usb.state") == "fastboot" {
				log.Print("fastbootd activo: usa 'fastboot devices' en el PC")
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		log.Printf("fastbootd: sys.usb.state=%q ffs.ready=%q (revisa dmesg)",
			getprop("sys.usb.state"), getprop("sys.usb.ffs.ready"))
	})
}

func apiFastbootd(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat("/system/bin/fastbootd"); err != nil && !*flagDev {
		fail(w, 500, "fastbootd no está en el ramdisk: %v", err)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		enterFastbootd()
	}()
}
