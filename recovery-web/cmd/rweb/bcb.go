package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"syscall"
)

// Bootloader Control Block (AOSP bootloader_message) al inicio de la partición misc.
// Mientras "command" sea boot-recovery/boot-fastboot, el bootloader arranca recovery.
const bcbSize = 2048

type bcb struct {
	Command  string `json:"command"`
	Status   string `json:"status"`
	Recovery string `json:"recovery"`
}

func miscDev() (string, error) {
	p, err := findPart("misc")
	if err != nil {
		return "", err
	}
	return p.Dev, nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func readBCB() (bcb, error) {
	dev, err := miscDev()
	if err != nil {
		return bcb{}, err
	}
	f, err := os.Open(dev)
	if err != nil {
		return bcb{}, err
	}
	defer f.Close()
	buf := make([]byte, 64+768)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return bcb{}, err
	}
	return bcb{cstr(buf[0:32]), cstr(buf[32:64]), cstr(buf[64:832])}, nil
}

// clearBCB deja el BCB vacío, igual que clear_bootloader_message() del recovery AOSP.
func clearBCB() error {
	dev, err := miscDev()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteAt(make([]byte, bcbSize), 0); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	log.Print("BCB de misc limpiado")
	return nil
}

// stockRequest indica si el BCB contiene una petición para el recovery stock.
func stockRequest(b bcb) ([]string, bool) {
	args := strings.Fields(strings.TrimPrefix(strings.TrimSpace(b.Recovery), "recovery"))
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return args, true
		}
	}
	return args, b.Command == "boot-fastboot"
}

// handoffToStock: si el BCB pide algo que solo sabe hacer el recovery de Samsung
// (fastbootd, OTA, borrado pedido desde Ajustes...), se le cede el control.
// Si no vuelve (exec correcto), esta función no retorna.
func handoffToStock() {
	if *flagDev {
		return
	}
	b, err := readBCB()
	if err != nil {
		log.Printf("BCB: %v", err)
		return
	}
	log.Printf("BCB: command=%q recovery=%q", b.Command, strings.ReplaceAll(b.Recovery, "\n", " "))
	args, ok := stockRequest(b)
	if !ok {
		return
	}
	const stock = "/system/bin/recovery.samsung"
	log.Printf("cediendo el control al recovery stock (%s %v)", b.Command, args)
	if err := syscall.Exec(stock, []string{"/system/bin/recovery"}, os.Environ()); err != nil {
		log.Printf("no se pudo lanzar %s: %v; limpiando BCB para no quedar en bucle", stock, err)
		if err := clearBCB(); err != nil {
			log.Printf("limpiar BCB: %v", err)
		}
	}
}
