# Pruebas: SukiSU como módulo (LKM) para el Galaxy A25 (SM-A256E)

El workflow `.github/workflows/build-sukisu-lkm.yml` compila **SukiSU** como módulo cargable `kernelsu.ko` (modo LKM) contra el código del kernel del SM-A256E (Linux 5.10.223, android12-5.10, Exynos 1280).

## Qué hace

1. Descarga el kernel de [TechExplorera/SM-A256E](https://github.com/TechExplorera/SM-A256E) (por defecto `main`, el código original de Samsung) y SukiSU-Ultra (rama `main`, ahora SukiSU-Nova, que trae `Kbuild` para LKM).
2. Compila el kernel completo con `s5e8825-a25xdxx_defconfig` usando el clang oficial de AOSP `clang-r416183b`. Esto genera el `Module.symvers` real, con el que `modpost` comprueba que existan todos los símbolos que usa el módulo y genera los CRC de `CONFIG_MODVERSIONS`.
3. Compila `kernelsu.ko` con `CONFIG_KSU=m` como módulo externo (`make M=...`).
4. Sube un artefacto con `kernelsu-a256e.ko` (sin símbolos de depuración), `modinfo.txt`, la `.config` del kernel y los logs.

## Cómo ejecutarlo

Se ejecuta en cada push. Para elegir otra versión: **Actions → Build SukiSU LKM (A256E) → Run workflow**.

| Entrada | Por defecto |
|---|---|
| `kernel_repo` | `TechExplorera/SM-A256E` |
| `kernel_ref` | `main` |
| `defconfig` | `s5e8825-a25xdxx_defconfig` |
| `sukisu_ref` | `c60f21b` (rama `main` de SukiSU-Ultra, 16/11/2026) |

## Notas

- En modo LKM, SukiSU usa kprobes y hooks de syscalls, no los parches de SuSFS. La config original de Samsung tiene `CONFIG_KPROBES=y` y no exige módulos firmados (`CONFIG_MODULE_SIG` desactivado).
- El módulo se instala con la app SukiSU-Nova Manager, eligiendo el modo LKM y parcheando el `boot.img`.
- No se ha probado todavía en el teléfono.
