# Recovery Web — Galaxy A25 5G (SM-A256E, `a25x`)

Recovery sin pantalla que se controla desde el navegador del PC por USB.
Se construye sobre el **recovery oficial de Samsung** (`A256EXXU7CYE1`): kernel, dtb, dtbo e init
son los originales; solo se sustituye `/system/bin/recovery` por `rweb`.

## Estado
| Parte | Estado |
|---|---|
| Daemon `rweb` (Go, estático arm64) | Compila; API probada en PC (modo `-dev`) |
| DHCP mínimo | Test unitario OK |
| Empaquetado | Reconstruye el stock byte a byte; imagen verificada |
| Arranque en el teléfono | v0.1 probado: arranca, red USB y panel funcionan |
| Salir de recovery / fastbootd | v0.2: limpia el BCB de `misc` y cede fastbootd/OTA/borrados al recovery stock (sin probar) |
| SELinux | Dominio `recovery` marcado permissive en `/sepolicy` (el resto sigue enforcing) |

## Funciones del panel (`http://192.168.90.1`)
- Info del dispositivo y backup rápido (EFS, sec_efs, param, boot, vbmeta, dtbo…)
- Particiones: backup (descarga) y flasheo de `.img` (con confirmación, control de tamaño y de compresión)
- Explorador de archivos con descarga y subida
- Terminal root (`sh -c`)
- Logs: rweb, dmesg, last_kmsg/pstore, getprop
- Reinicio: sistema, recovery, download, fastbootd, apagar
- Recovery stock de Samsung: menú en pantalla, borrar caché, restablecer de fábrica

## Orden de arranque (BCB)
Android y el bootloader se comunican con el recovery a través de los primeros 2048 bytes de la partición `misc`.
Mientras ahí haya `boot-recovery` o `boot-fastboot`, el teléfono arranca recovery en cada reinicio.
- Al arrancar, si el BCB pide fastbootd, una OTA o un borrado (`--fastboot`, `--wipe_data`, `--update_package`…), rweb cede el control al recovery de Samsung, que sabe hacerlo y limpia el BCB al terminar.
- Todos los botones de reinicio limpian el BCB antes de reiniciar.
- En *Reinicio y avanzado* se puede leer y limpiar el BCB a mano.

## Requisitos
- Bootloader desbloqueado y `vbmeta` con verificación desactivada (ya lo tienes si usaste TWRP).
- Firmware instalado `A256EXXU7CYE1` (bootloader U7).

## Instalación (Odin)
1. Apaga y entra en modo Download (Vol+ y Vol− a la vez, y conecta el cable USB).
2. Odin → **AP** → `out/recovery.tar.md5`. Desmarca *Auto Reboot* → Start.
3. Mantén **Vol+ y Power** hasta que entre en recovery. Si no entras directamente, Samsung puede restaurar el recovery stock al arrancar el sistema.
4. Conecta el USB al PC y abre `http://192.168.90.1`. El PC recibe la IP 192.168.90.2 por DHCP.
   - Windows: RNDIS se instala solo gracias a los descriptores MS OS. Si no, instala el driver "Remote NDIS compatible device".
   - Linux: funciona directamente. macOS no soporta RNDIS.
5. La pantalla del móvil puede quedar en negro: es lo esperado.

## Volver atrás
Odin → AP → `out/stock-recovery-A256EXXU7CYE1.tar.md5`, o reflashea tu TWRP.

## Compilar
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o build/rweb-arm64 ./cmd/rweb
pip install lz4
python3 tools/build_recovery.py <recovery.img o .img.lz4 stock> build/rweb-arm64 out
```
Prueba en PC: `go run ./cmd/rweb -dev -root /ruta/con/dev-parts -listen 127.0.0.1:8080`.

## SELinux
`rweb` corre con el contexto `u:r:recovery:s0` del init stock, que en la política de Samsung es *enforcing*.
`tools/sepolicy_permissive.c` (contra libsepol estática) marca solo ese dominio como permissive, igual que TWRP:
```bash
gcc -O2 -I libsepol/include -I libsepol/src tools/sepolicy_permissive.c libsepol/src/libsepol.a -o build/sepolicy_permissive
build/sepolicy_permissive <ramdisk>/sepolicy build/sepolicy.patched recovery
python3 tools/build_recovery.py <stock> build/rweb-arm64 out --sepolicy build/sepolicy.patched
```
libsepol se compila con `make -C libsepol/src DISABLE_CIL=y libsepol.a`.

## Seguridad
- El panel solo escucha en la interfaz USB.
- Las acciones de escritura exigen POST con la cabecera `X-RWeb`, lo que impide que otra web abierta las dispare.
- Flasheo y borrado piden confirmación escrita.

## Descargas (carpeta `dist/`)
| Archivo | Uso |
|---|---|
| [`recovery.tar.md5`](https://github.com/TechExplorera/test/raw/recovery-web/recovery-web/dist/recovery.tar.md5) | Recovery Web para Odin (AP) |
| [`stock-recovery-A256EXXU7CYE1.tar.md5`](https://github.com/TechExplorera/test/raw/recovery-web/recovery-web/dist/stock-recovery-A256EXXU7CYE1.tar.md5) | Recovery oficial de Samsung (`recovery.img.lz4` sin modificar) para volver atrás |
| [`SHA256SUMS`](dist/SHA256SUMS) | Hashes para comprobar la descarga |
