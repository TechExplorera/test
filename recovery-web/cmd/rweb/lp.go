package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// Particiones dinámicas (metadatos LP dentro de "super").
// Se lee la copia primaria del slot 0; nunca se modifican los metadatos:
// flashear una partición lógica escribe dentro de sus extents existentes.

const (
	lpGeometryMagic = 0x616C4467
	lpMetadataMagic = 0x414C5030
	lpSector        = 512
)

type lpExtent struct {
	Offset int64 `json:"offset"` // bytes dentro de super
	Size   int64 `json:"size"`
}

type lpPartition struct {
	Name    string     `json:"name"`
	Group   string     `json:"group"`
	Size    int64      `json:"size"`
	Extents []lpExtent `json:"extents"`
	FS      string     `json:"fs"`
}

type lpLayout struct {
	SuperDev   string        `json:"super_dev"`
	SuperSize  int64         `json:"super_size"`
	Used       int64         `json:"used"`
	Partitions []lpPartition `json:"partitions"`
}

func readLP() (*lpLayout, error) {
	p, err := findPart("super")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p.Dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	geo := make([]byte, 52)
	if _, err := f.ReadAt(geo, 4096); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(geo) != lpGeometryMagic {
		return nil, fmt.Errorf("no hay geometría LP en super")
	}
	maxSize := binary.LittleEndian.Uint32(geo[40:])
	if maxSize == 0 || maxSize > 1<<20 {
		return nil, fmt.Errorf("tamaño de metadatos LP no válido: %d", maxSize)
	}
	md := make([]byte, maxSize)
	if _, err := f.ReadAt(md, 4096*3); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(md) != lpMetadataMagic {
		return nil, fmt.Errorf("no hay cabecera de metadatos LP")
	}
	hsize := binary.LittleEndian.Uint32(md[8:])
	tsize := binary.LittleEndian.Uint32(md[44:])
	if int(hsize)+int(tsize) > len(md) {
		return nil, fmt.Errorf("metadatos LP truncados")
	}
	t := md[hsize : hsize+tsize]
	desc := func(i int) (off, num, sz uint32) {
		b := md[80+12*i:]
		return binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:])
	}
	cstr := func(b []byte) string { return strings.TrimRight(string(b), "\x00") }

	eo, en, es := desc(1)
	type ext struct {
		sectors uint64
		typ     uint32
		data    uint64
	}
	exts := make([]ext, en)
	for i := range exts {
		b := t[eo+uint32(i)*es:]
		exts[i] = ext{binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint32(b[8:]), binary.LittleEndian.Uint64(b[12:])}
	}
	gro, grn, grs := desc(2)
	groups := make([]string, grn)
	for i := range groups {
		groups[i] = cstr(t[gro+uint32(i)*grs:][:36])
	}

	lay := &lpLayout{SuperDev: p.Dev, SuperSize: p.Size}
	po, pn, ps := desc(0)
	for i := uint32(0); i < pn; i++ {
		b := t[po+i*ps:]
		lp := lpPartition{Name: cstr(b[:36])}
		first := binary.LittleEndian.Uint32(b[40:])
		num := binary.LittleEndian.Uint32(b[44:])
		gi := binary.LittleEndian.Uint32(b[48:])
		if int(gi) < len(groups) {
			lp.Group = groups[gi]
		}
		for j := first; j < first+num && int(j) < len(exts); j++ {
			e := exts[j]
			if e.typ != 0 { // solo LINEAR
				return nil, fmt.Errorf("%s: extent de tipo %d no soportado", lp.Name, e.typ)
			}
			x := lpExtent{int64(e.data) * lpSector, int64(e.sectors) * lpSector}
			lp.Extents = append(lp.Extents, x)
			lp.Size += x.Size
		}
		if len(lp.Extents) > 0 {
			lp.FS = detectFS(f, lp.Extents[0].Offset)
		}
		lay.Used += lp.Size
		lay.Partitions = append(lay.Partitions, lp)
	}
	return lay, nil
}

func detectFS(f *os.File, off int64) string {
	b := make([]byte, 2048)
	if _, err := f.ReadAt(b, off); err != nil {
		return "?"
	}
	switch {
	case binary.LittleEndian.Uint32(b[1024:]) == 0xE0F5E1E2:
		return "erofs"
	case binary.LittleEndian.Uint16(b[1080:]) == 0xEF53:
		return "ext4"
	case binary.LittleEndian.Uint32(b[1024:]) == 0xF2F52010:
		return "f2fs"
	}
	return "?"
}

func findLP(name string) (*lpLayout, *lpPartition, error) {
	lay, err := readLP()
	if err != nil {
		return nil, nil, err
	}
	for i := range lay.Partitions {
		if lay.Partitions[i].Name == name {
			return lay, &lay.Partitions[i], nil
		}
	}
	return nil, nil, fmt.Errorf("partición lógica %q no encontrada", name)
}

func apiLP(w http.ResponseWriter, r *http.Request) {
	lay, err := readLP()
	if err != nil {
		fail(w, 500, "leer super: %v", err)
		return
	}
	jsonOut(w, lay)
}

func apiLPDump(w http.ResponseWriter, r *http.Request) {
	lay, lp, err := findLP(r.URL.Query().Get("name"))
	if err != nil {
		fail(w, 404, "%v", err)
		return
	}
	f, err := os.Open(lay.SuperDev)
	if err != nil {
		fail(w, 500, "%v", err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.img"`, lp.Name))
	w.Header().Set("Content-Length", fmt.Sprint(lp.Size))
	var total int64
	for _, e := range lp.Extents {
		n, err := io.Copy(w, io.NewSectionReader(f, e.Offset, e.Size))
		total += n
		if err != nil {
			log.Printf("backup lógico %s: %v", lp.Name, err)
			return
		}
	}
	log.Printf("backup lógico %s: %d bytes", lp.Name, total)
}

func apiLPFlash(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" || r.Header.Get("X-Confirm") != name {
		fail(w, 400, "falta confirmación: escribe el nombre exacto de la partición")
		return
	}
	lay, lp, err := findLP(name)
	if err != nil {
		fail(w, 404, "%v", err)
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > lp.Size {
		fail(w, 400, "la imagen (%d bytes) no cabe en %s (%d bytes)", r.ContentLength, name, lp.Size)
		return
	}
	if !flashMu.TryLock() {
		fail(w, 409, "ya hay un flasheo en curso")
		return
	}
	defer flashMu.Unlock()

	br := bufio.NewReaderSize(r.Body, 1<<20)
	head, err := br.Peek(2048)
	if err != nil {
		fail(w, 400, "imagen demasiado pequeña")
		return
	}
	if binary.LittleEndian.Uint32(head) == 0xED26FF3A {
		fail(w, 400, "imagen sparse: conviértela a raw primero (simg2img)")
		return
	}
	fs := "?"
	switch {
	case binary.LittleEndian.Uint32(head[1024:]) == 0xE0F5E1E2:
		fs = "erofs"
	case binary.LittleEndian.Uint16(head[1080:]) == 0xEF53:
		fs = "ext4"
	case binary.LittleEndian.Uint32(head[1024:]) == 0xF2F52010:
		fs = "f2fs"
	}
	if fs == "?" {
		fail(w, 400, "no es una imagen EROFS, ext4 ni f2fs")
		return
	}

	f, err := os.OpenFile(lay.SuperDev, os.O_WRONLY, 0)
	if err != nil {
		fail(w, 500, "abrir super: %v", err)
		return
	}
	defer f.Close()
	log.Printf("flasheando lógica %s (%s, %d bytes) en %d extents", name, fs, r.ContentLength, len(lp.Extents))
	left := r.ContentLength
	var written int64
	for _, e := range lp.Extents {
		if left == 0 {
			break
		}
		n := e.Size
		if n > left {
			n = left
		}
		c, err := io.CopyN(io.NewOffsetWriter(f, e.Offset), br, n)
		written += c
		left -= c
		if err != nil {
			fail(w, 500, "error tras escribir %d bytes: %v", written, err)
			return
		}
	}
	if err := f.Sync(); err != nil {
		fail(w, 500, "sync: %v", err)
		return
	}
	syscall.Sync()
	log.Printf("flasheada lógica %s OK (%d bytes)", name, written)
	jsonOut(w, map[string]any{"ok": true, "written": written, "fs": fs})
}

// ---------- vbmeta ----------

type vbmetaInfo struct {
	Name     string `json:"name"`
	Present  bool   `json:"present"`
	Flags    uint32 `json:"flags"`
	Disabled bool   `json:"verification_disabled"`
	NoVerity bool   `json:"hashtree_disabled"`
}

func readVbmeta(name string) vbmetaInfo {
	vi := vbmetaInfo{Name: name}
	p, err := findPart(name)
	if err != nil {
		return vi
	}
	f, err := os.Open(p.Dev)
	if err != nil {
		return vi
	}
	defer f.Close()
	b := make([]byte, 128)
	if _, err := f.ReadAt(b, 0); err != nil || string(b[:4]) != "AVB0" {
		return vi
	}
	vi.Present = true
	vi.Flags = binary.BigEndian.Uint32(b[120:])
	vi.NoVerity = vi.Flags&1 != 0
	vi.Disabled = vi.Flags&2 != 0
	return vi
}

func apiVbmeta(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, []vbmetaInfo{readVbmeta("vbmeta"), readVbmeta("vbmeta_system")})
}
