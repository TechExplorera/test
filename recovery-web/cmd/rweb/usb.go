package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const gadget = "/config/usb_gadget/g1"

func wr(path, val string) error {
	err := os.WriteFile(path, []byte(val), 0644)
	if err != nil {
		return fmt.Errorf("escribir %s=%q: %w", path, val, err)
	}
	return nil
}

func rd(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// setupUSB reconfigura el gadget configfs creado por init como RNDIS
// (con descriptores de Microsoft para que Windows cargue el driver solo).
// Devuelve el nombre de la interfaz de red (rndis0/usb0).
func setupUSB() (string, error) {
	// init monta configfs en "on fs"; esperar hasta 20 s.
	for i := 0; i < 40; i++ {
		if _, err := os.Stat(gadget); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if _, err := os.Stat(gadget); err != nil {
		return "", fmt.Errorf("configfs no disponible: %w", err)
	}

	udc := rd("/config/usb_gadget/g1/UDC")
	if udc != "" && udc != "none" {
		_ = wr(gadget+"/UDC", "\n")
	}

	cfg := gadget + "/configs/b.1"
	// Quitar funciones enlazadas (adb/fastboot) de la configuración.
	if ents, err := os.ReadDir(cfg); err == nil {
		for _, e := range ents {
			p := filepath.Join(cfg, e.Name())
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(p)
			}
		}
	}
	_ = os.Remove(gadget + "/os_desc/b.1")

	fn := gadget + "/functions/rndis.rweb"
	if err := os.Mkdir(fn, 0770); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("crear función RNDIS: %w", err)
	}

	steps := [][2]string{
		{gadget + "/idVendor", "0x04e8"},  // Samsung
		{gadget + "/idProduct", "0x6863"}, // Samsung RNDIS
		{gadget + "/bcdDevice", "0x0400"},
		{gadget + "/bDeviceClass", "0x00"},
		{cfg + "/strings/0x409/configuration", "rweb"},
		{gadget + "/strings/0x409/product", "Recovery Web"},
	}
	for _, s := range steps {
		if err := wr(s[0], s[1]); err != nil {
			log.Print(err)
		}
	}
	// Descriptores OS de Microsoft: Windows asigna el driver RNDIS sin instalar nada.
	if _, err := os.Stat(gadget + "/os_desc"); err == nil {
		_ = wr(gadget+"/os_desc/use", "1")
		_ = wr(gadget+"/os_desc/b_vendor_code", "0xcd")
		_ = wr(gadget+"/os_desc/qw_sign", "MSFT100")
		_ = wr(fn+"/os_desc/interface.rndis/compatible_id", "RNDIS")
		_ = wr(fn+"/os_desc/interface.rndis/sub_compatible_id", "5162001")
		_ = os.Symlink(cfg, gadget+"/os_desc/b.1")
	}
	if err := os.Symlink(fn, cfg+"/f1"); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("enlazar función: %w", err)
	}

	if udc == "" || udc == "none" {
		udc = firstUDC()
	}
	if udc == "" {
		return "", fmt.Errorf("no se encontró controlador USB en /sys/class/udc")
	}
	if err := wr(gadget+"/UDC", udc); err != nil {
		return "", err
	}
	log.Printf("USB RNDIS activo en %s", udc)

	for i := 0; i < 20; i++ {
		if n := rd(fn + "/ifname"); n != "" && !strings.Contains(n, "unnamed") {
			return n, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	for _, n := range []string{"rndis0", "usb0"} {
		if _, err := os.Stat("/sys/class/net/" + n); err == nil {
			return n, nil
		}
	}
	return "", fmt.Errorf("no apareció la interfaz de red RNDIS")
}

func firstUDC() string {
	ents, _ := os.ReadDir("/sys/class/udc")
	for _, e := range ents {
		return e.Name()
	}
	return ""
}
