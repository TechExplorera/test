#!/bin/bash
# Reconstruye una partición EROFS de Android: extrae, elimina rutas, restaura
# metadatos (dueño, permisos, SELinux, capabilities), comprime con LZ4HC y verifica.
#
# Uso: build_partition.sh ORIGINAL.img LISTA_A_ELIMINAR.txt SALIDA.img TRABAJO_DIR
set -euo pipefail
ORIG=$1; LIST=$2; OUT=$3; WORK=$4
TOOLS=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$WORK" "$(dirname "$OUT")"
META="$WORK/meta.json"; SRC="$WORK/src"; CHK="$WORK/check"

echo "[1/6] metadatos del original"
python3 -I "$TOOLS/erofs_meta.py" dump "$ORIG" "$META"

echo "[2/6] extracción"
[ -e "$SRC" ] && { echo "ya existe $SRC; bórralo antes"; exit 1; }
mkdir -p "$SRC"
fsck.erofs --extract="$SRC" --preserve "$ORIG"

echo "[3/6] eliminación"
while read -r p; do
  [ -z "$p" ] && continue
  case "$p" in /*) ;; *) echo "ruta no absoluta: $p"; exit 1;; esac
  case "$p" in *..*) echo "ruta no válida: $p"; exit 1;; esac
  if [ -e "$SRC$p" ]; then
    rm -r -- "$SRC$p"
    echo "  - $p"
  else
    echo "  (no existe) $p"
  fi
done < "$LIST"

echo "[4/6] restaurar dueño, permisos y xattrs"
python3 -I "$TOOLS/erofs_meta.py" apply "$META" "$SRC"

echo "[5/6] mkfs.erofs lz4hc"
mkfs.erofs -zlz4hc,12 -T1640995200 --mount-point=/ "$OUT" "$SRC" | tail -3

echo "[6/6] verificación"
fsck.erofs "$OUT"
python3 -I "$TOOLS/erofs_meta.py" verify "$META" "$OUT" --removed "$LIST"
mkdir -p "$CHK"
fsck.erofs --extract="$CHK" "$OUT"
if diff -r --no-dereference -q "$SRC" "$CHK"; then
  echo "contenido idéntico al árbol fuente"
else
  echo "ERROR: el contenido difiere"; exit 1
fi
ls -la "$ORIG" "$OUT" | awk '{printf "%8.0f MiB  %s\n",$5/1048576,$9}'
sha256sum "$OUT"
echo "LISTO"
