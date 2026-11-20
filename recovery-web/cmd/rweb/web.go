package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const version = "0.2.0"

//go:embed web
var webFS embed.FS

// Solo una escritura a bloque a la vez.
var flashMu sync.Mutex

type partition struct {
	Name string `json:"name"`
	Dev  string `json:"dev"`
	Size int64  `json:"size"`
}

func serveHTTP(addr string) error {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/info", apiInfo)
	mux.HandleFunc("/api/parts", apiParts)
	mux.HandleFunc("/api/dump", apiDump)
	mux.HandleFunc("/api/flash", apiFlash)
	mux.HandleFunc("/api/shell", apiShell)
	mux.HandleFunc("/api/ls", apiLs)
	mux.HandleFunc("/api/file", apiFile)
	mux.HandleFunc("/api/upload", apiUpload)
	mux.HandleFunc("/api/log", apiLog)
	mux.HandleFunc("/api/reboot", apiReboot)
	mux.HandleFunc("/api/stock", apiStock)
	mux.HandleFunc("/api/bcb", apiBCB)
	srv := &http.Server{Addr: addr, Handler: guard(mux), ReadHeaderTimeout: 15 * time.Second}
	log.Printf("HTTP escuchando en http://%s", addr)
	return srv.ListenAndServe()
}

// guard: las peticiones que modifican algo deben ser POST y venir del propio panel
// (cabecera X-RWeb), lo que impide que otra web abierta en el PC las dispare.
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			if r.Method != http.MethodPost || r.Header.Get("X-RWeb") != "1" {
				http.Error(w, "petición no permitida", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	log.Printf("error %d: %s", code, msg)
	http.Error(w, msg, code)
}

func rootPath(p string) string {
	return filepath.Join(*flagRoot, filepath.Clean("/"+p))
}

// ---------- Información ----------

func getprop(name string) string {
	out, err := exec.Command("/system/bin/getprop", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func apiInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]string{"rweb": version}
	for _, p := range []string{"ro.product.model", "ro.product.device", "ro.build.PDA",
		"ro.bootloader", "ro.build.version.release", "ro.boot.warranty_bit",
		"ro.boot.verifiedbootstate", "ro.boot.flash.locked", "ro.serialno"} {
		info[p] = getprop(p)
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		info["kernel"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/sys/fs/selinux/enforce"); err == nil {
		info["selinux_enforce"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/sys/class/power_supply/battery/capacity"); err == nil {
		info["battery"] = strings.TrimSpace(string(b)) + "%"
	}
	jsonOut(w, info)
}

// ---------- Particiones ----------

func listParts() ([]partition, error) {
	if *flagDev {
		ents, err := os.ReadDir(rootPath("dev-parts"))
		if err != nil {
			return nil, err
		}
		var ps []partition
		for _, e := range ents {
			fi, _ := e.Info()
			ps = append(ps, partition{strings.TrimSuffix(e.Name(), ".img"), rootPath("dev-parts/" + e.Name()), fi.Size()})
		}
		return ps, nil
	}
	ents, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil, err
	}
	var ps []partition
	for _, e := range ents {
		ue := rd("/sys/class/block/" + e.Name() + "/uevent")
		var name, dev string
		for _, l := range strings.Split(ue, "\n") {
			if v, ok := strings.CutPrefix(l, "PARTNAME="); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(l, "DEVNAME="); ok {
				dev = "/dev/block/" + v
			}
		}
		if name == "" || dev == "" {
			continue
		}
		sz, _ := strconv.ParseInt(rd("/sys/class/block/"+e.Name()+"/size"), 10, 64)
		ps = append(ps, partition{name, dev, sz * 512})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	return ps, nil
}

func findPart(name string) (partition, error) {
	ps, err := listParts()
	if err != nil {
		return partition{}, err
	}
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return partition{}, fmt.Errorf("partición %q no encontrada", name)
}

func blkSize(f *os.File) int64 {
	var sz uint64
	const blkgetsize64 = 0x80081272
	if ioctl(int(f.Fd()), blkgetsize64, unsafe.Pointer(&sz)) == nil && sz > 0 {
		return int64(sz)
	}
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}

func apiParts(w http.ResponseWriter, r *http.Request) {
	ps, err := listParts()
	if err != nil {
		fail(w, 500, "listar particiones: %v", err)
		return
	}
	jsonOut(w, ps)
}

func apiDump(w http.ResponseWriter, r *http.Request) {
	p, err := findPart(r.URL.Query().Get("name"))
	if err != nil {
		fail(w, 404, "%v", err)
		return
	}
	f, err := os.Open(p.Dev)
	if err != nil {
		fail(w, 500, "abrir %s: %v", p.Dev, err)
		return
	}
	defer f.Close()
	size := blkSize(f)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.img"`, p.Name))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	n, err := io.CopyN(w, f, size)
	log.Printf("backup %s: %d bytes (%v)", p.Name, n, err)
}

var magicCompressed = map[string][]byte{
	"LZ4 (descomprímelo primero)":  {0x04, 0x22, 0x4d, 0x18},
	"LZ4 legacy (descomprímelo)":   {0x02, 0x21, 0x4c, 0x18},
	"ZIP (extrae la .img)":         {'P', 'K', 3, 4},
	"gzip (descomprímelo primero)": {0x1f, 0x8b},
	"XZ (descomprímelo primero)":   {0xfd, '7', 'z', 'X', 'Z'},
}

func apiFlash(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if r.Header.Get("X-Confirm") != name || name == "" {
		fail(w, 400, "falta confirmación: escribe el nombre exacto de la partición")
		return
	}
	p, err := findPart(name)
	if err != nil {
		fail(w, 404, "%v", err)
		return
	}
	if r.ContentLength <= 0 {
		fail(w, 400, "tamaño de imagen desconocido")
		return
	}
	if !flashMu.TryLock() {
		fail(w, 409, "ya hay un flasheo en curso")
		return
	}
	defer flashMu.Unlock()

	br := bufio.NewReaderSize(r.Body, 1<<20)
	head, _ := br.Peek(8)
	for desc, m := range magicCompressed {
		if len(head) >= len(m) && string(head[:len(m)]) == string(m) {
			fail(w, 400, "el archivo está comprimido: %s", desc)
			return
		}
	}
	f, err := os.OpenFile(p.Dev, os.O_WRONLY, 0)
	if err != nil {
		fail(w, 500, "abrir %s: %v", p.Dev, err)
		return
	}
	defer f.Close()
	size := blkSize(f)
	if r.ContentLength > size {
		fail(w, 400, "la imagen (%d bytes) es mayor que la partición %s (%d bytes)", r.ContentLength, name, size)
		return
	}
	log.Printf("flasheando %s (%d bytes) en %s", name, r.ContentLength, p.Dev)
	n, err := io.CopyN(f, br, r.ContentLength)
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		fail(w, 500, "error tras escribir %d bytes: %v", n, err)
		return
	}
	syscall.Sync()
	log.Printf("flasheado %s OK (%d bytes)", name, n)
	jsonOut(w, map[string]any{"ok": true, "written": n})
}

// ---------- Shell ----------

func apiShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd     string `json:"cmd"`
		Timeout int    `json:"timeout"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Cmd == "" {
		fail(w, 400, "comando vacío")
		return
	}
	if req.Timeout <= 0 || req.Timeout > 600 {
		req.Timeout = 60
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()
	sh := "/system/bin/sh"
	if *flagDev {
		sh = "/bin/sh"
	}
	cmd := exec.CommandContext(ctx, sh, "-c", req.Cmd)
	cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin:/sbin:/bin")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		out = append(out, []byte(fmt.Sprintf("\n[tiempo agotado: %ds]", req.Timeout))...)
	}
	log.Printf("shell (%d): %s", code, req.Cmd)
	jsonOut(w, map[string]any{"code": code, "out": string(out)})
}

// ---------- Archivos ----------

type entry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Link  string `json:"link,omitempty"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
	MTime int64  `json:"mtime"`
}

func apiLs(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	ents, err := os.ReadDir(rootPath(p))
	if err != nil {
		fail(w, 400, "%v", err)
		return
	}
	out := []entry{}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		en := entry{Name: e.Name(), Size: fi.Size(), Mode: fi.Mode().String(), MTime: fi.ModTime().Unix()}
		full := filepath.Join(rootPath(p), e.Name())
		if fi.Mode()&os.ModeSymlink != 0 {
			en.Link, _ = os.Readlink(full)
			if st, err := os.Stat(full); err == nil {
				en.Dir = st.IsDir()
			}
		} else {
			en.Dir = fi.IsDir()
		}
		out = append(out, en)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	jsonOut(w, out)
}

func apiFile(w http.ResponseWriter, r *http.Request) {
	p := rootPath(r.URL.Query().Get("path"))
	f, err := os.Open(p)
	if err != nil {
		fail(w, 404, "%v", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		fail(w, 400, "no es un archivo")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(p)))
	if fi.Mode().IsRegular() && fi.Size() > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	_, _ = io.Copy(w, f)
}

func apiUpload(w http.ResponseWriter, r *http.Request) {
	p := rootPath(r.URL.Query().Get("path"))
	tmp := p + ".rweb-part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fail(w, 500, "%v", err)
		return
	}
	n, err := io.Copy(f, r.Body)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		fail(w, 500, "subida: %v", err)
		return
	}
	log.Printf("subido %s (%d bytes)", p, n)
	jsonOut(w, map[string]any{"ok": true, "written": n})
}

// ---------- Logs ----------

func apiLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	switch r.URL.Query().Get("src") {
	case "dmesg":
		out, err := exec.Command("/system/bin/dmesg").CombinedOutput()
		if err != nil {
			out = append(out, []byte("\n"+err.Error())...)
		}
		_, _ = w.Write(out)
	case "last_kmsg":
		for _, p := range []string{"/proc/last_kmsg", "/sys/fs/pstore/console-ramoops-0", "/sys/fs/pstore/console-ramoops"} {
			if b, err := os.ReadFile(p); err == nil {
				fmt.Fprintf(w, "== %s ==\n%s", p, b)
				return
			}
		}
		fmt.Fprintln(w, "no disponible")
	case "props":
		out, _ := exec.Command("/system/bin/getprop").CombinedOutput()
		_, _ = w.Write(out)
	default:
		b, _ := os.ReadFile("/tmp/rweb.log")
		_, _ = w.Write(b)
	}
}

// ---------- Reinicio ----------

func apiReboot(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	targets := map[string]string{
		"system":     "reboot,",
		"recovery":   "reboot,recovery",
		"download":   "reboot,download",
		"bootloader": "reboot,bootloader",
		"fastboot":   "reboot,fastboot",
		"poweroff":   "shutdown,",
	}
	val, ok := targets[mode]
	if !ok {
		fail(w, 400, "modo desconocido")
		return
	}
	jsonOut(w, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	log.Printf("reinicio: %s", mode)
	if *flagDev {
		return
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		// Sin esto el bootloader vuelve a arrancar recovery si quedó una orden en misc.
		// Para fastboot, init vuelve a escribir boot-fastboot después.
		if err := clearBCB(); err != nil {
			log.Printf("limpiar BCB antes de reiniciar: %v", err)
		}
		syscall.Sync()
		_ = exec.Command("/system/bin/setprop", "sys.powerctl", val).Run()
		time.Sleep(5 * time.Second)
		// Respaldo si init no atendió la propiedad.
		arg := strings.TrimPrefix(strings.TrimPrefix(val, "reboot,"), "shutdown,")
		if mode == "poweroff" {
			_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
		}
		b := append([]byte(arg), 0)
		_, _, _ = syscall.Syscall6(syscall.SYS_REBOOT, 0xfee1dead, 672274793,
			syscall.LINUX_REBOOT_CMD_RESTART2, uintptr(unsafe.Pointer(&b[0])), 0, 0)
	}()
}

// ---------- BCB (misc) ----------

func apiBCB(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := clearBCB(); err != nil {
			fail(w, 500, "limpiar BCB: %v", err)
			return
		}
	}
	b, err := readBCB()
	if err != nil {
		fail(w, 500, "leer BCB: %v", err)
		return
	}
	jsonOut(w, b)
}

// ---------- Recovery stock de Samsung ----------

// apiStock lanza el recovery original (pantalla y botones) con argumentos opcionales,
// p. ej. --wipe_data. Ojo: puede reconfigurar el USB y cortar el panel web.
func apiStock(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	args := map[string][]string{
		"ui":         {},
		"wipe_data":  {"--wipe_data"},
		"wipe_cache": {"--wipe_cache"},
	}
	a, ok := args[action]
	if !ok {
		fail(w, 400, "acción desconocida")
		return
	}
	if action == "wipe_data" && r.Header.Get("X-Confirm") != "BORRAR" {
		fail(w, 400, "falta confirmación")
		return
	}
	bin := "/system/bin/recovery.samsung"
	if _, err := os.Stat(bin); err != nil {
		fail(w, 500, "recovery stock no encontrado: %v", err)
		return
	}
	cmd := exec.Command(bin, a...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		fail(w, 500, "%v", err)
		return
	}
	log.Printf("recovery stock iniciado (pid %d) %v", cmd.Process.Pid, a)
	go func() { log.Printf("recovery stock terminó: %v", cmd.Wait()) }()
	jsonOut(w, map[string]any{"ok": true, "pid": cmd.Process.Pid})
}
