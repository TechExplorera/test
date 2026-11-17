# Pruebas: SukiSU como módulo (LKM) para el Galaxy A25 (SM-A256E)

El workflow `.github/workflows/build-sukisu-lkm.yml` compila **SukiSU** como módulo cargable `kernelsu.ko` (modo LKM) contra el código del kernel del SM-A256E (Linux 5.10.223, android12-5.10, Exynos 1280), sin tener que flashear un kernel nuevo.

## Cómo ejecutarlo

Se ejecuta en cada push. Para elegir versiones: **Actions → Build SukiSU LKM (A256E) → Run workflow**.

| Entrada | Por defecto | Qué es |
|---|---|---|
| `kernel_url` | `https://github.com/TechExplorera/SM-A256E` | URL git del código del kernel |
| `kernel_ref` | `main` | Rama/commit (main = código original de Samsung) |
| `defconfig` | `s5e8825-a25xdxx_defconfig` | Defconfig del kernel |
| `sukisu_url` | `https://github.com/SukiSU-Ultra/SukiSU-Ultra.git` | URL git de SukiSU |
| `sukisu_ref` | `c60f21b` | Commit/rama de SukiSU |
| `mode` | `full` | `full` o `fast` (ver abajo) |
| `export_cred_symbols` | `true` | Exportar los símbolos que SukiSU necesita (solo modo full) |

## Modos

- **`full`**: compila el kernel completo para obtener un `Module.symvers` real. Así `modpost` comprueba que existan todos los símbolos que usa el módulo y genera los CRC de `CONFIG_MODVERSIONS`. Es el único que produce un `.ko` cargable. ~25 min.
- **`fast`**: solo prepara el árbol (`modules_prepare` + SELinux) y compila el módulo con `KBUILD_MODPOST_WARN=1`. ~5 min. **No** comprueba símbolos, así que sirve para ver si el código del módulo compila, no para un `.ko` final.

## El detalle de los símbolos (importante)

SukiSU usa `prepare_creds()` y `put_cred()`. En el kernel de Samsung eso engancha con 3 símbolos que, por defecto, **no están disponibles para un módulo externo**:

- `prepare_creds`, `__put_cred`: están exportados en `kernel/cred.c`, pero `CONFIG_TRIM_UNUSED_KSYMS` los borra porque no están en la whitelist `gki/abi_symbollist.raw`.
- `kdp_usecount_dec_and_test`: es interno de la protección KDP de Samsung (`drivers/uh/kdp.c`); la macro `put_cred()` lo llama porque `CONFIG_KDP_CRED=y`.

Sin resolverlos, `modpost` corta la compilación con "undefined". El paso **Export cred symbols** los añade a la whitelist y exporta el de KDP, por eso el modo `full` necesita `export_cred_symbols=true`.

**Consecuencia para flashear:** como esos símbolos no están en el kernel de fábrica, este `.ko` solo carga limpio en un kernel compilado con ellos exportados (el que produce este mismo workflow en modo full). En el kernel **de fábrica** de Samsung, `insmod` fallaría con "unknown symbol". Por eso, para uso real en el teléfono, suele ser más fiable el método **integrado** (SukiSU compilado dentro del kernel), que está en la rama de trabajo del repo `SM-A256E`.

## Notas

- En modo LKM, SukiSU usa kprobes y hooks de syscalls, no los parches de SuSFS.
- La config original de Samsung tiene `CONFIG_KPROBES=y` y no exige módulos firmados (`CONFIG_MODULE_SIG` desactivado).
- No se ha probado todavía en el teléfono.
