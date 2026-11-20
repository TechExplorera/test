#!/usr/bin/env python3
"""Reactiva "Enter fastboot" en el recovery STOCK de Samsung.

El recovery de Samsung solo permite fastbootd si /proc/cmdline contiene
"androidboot.fastbootd=true" (el "fastbootd token"), que el bootloader de los
equipos de venta no pasa. Esta herramienta localiza la función que hace esa
comprobación (por su contenido, no por dirección fija) y la sustituye por
"return true" (mov w0, #1; ret). Todo lo demás del recovery queda igual:
menú en pantalla, SELinux enforcing y política sin tocar.

Uso: stock_fastboot.py STOCK_recovery.img(.lz4) OUT_DIR
Requiere: pip install lz4 capstone pyelftools
"""
import hashlib, io, os, struct, sys, tarfile, time
import importlib.util
from elftools.elf.elffile import ELFFile
from capstone import Cs, CS_ARCH_ARM64, CS_MODE_ARM

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location('br', os.path.join(HERE, 'build_recovery.py'))
br = importlib.util.module_from_spec(spec); spec.loader.exec_module(br)

TOKEN = b'androidboot.fastbootd=true'
MSG_OK = b'the device has a fastbootd token'
PATCH = struct.pack('<II', 0x52800020, 0xD65F03C0)  # mov w0, #1 ; ret


def find_check(binary):
    elf = ELFFile(io.BytesIO(binary))
    t = elf.get_section_by_name('.text')
    code, base, toff = t.data(), t['sh_addr'], t['sh_offset']
    s = binary.find(MSG_OK)
    if s < 0:
        raise SystemExit('no se encontró el mensaje del token: ¿recovery no Samsung o ya parcheado?')

    def off2va(off):
        for seg in elf.iter_segments():
            if seg['p_type'] == 'PT_LOAD' and seg['p_offset'] <= off < seg['p_offset'] + seg['p_filesz']:
                return seg['p_vaddr'] + off - seg['p_offset']
    msg_va = off2va(s)
    md = Cs(CS_ARCH_ARM64, CS_MODE_ARM)
    insns = list(md.disasm(code, base))
    regs, ref = {}, None
    for i, ins in enumerate(insns):
        if ins.mnemonic == 'adrp':
            r, imm = ins.op_str.split(', ')
            regs[r] = int(imm.lstrip('#'), 16)
        elif ins.mnemonic == 'add' and '#' in ins.op_str:
            p = [x.strip() for x in ins.op_str.split(',')]
            if len(p) == 3 and p[1] in regs and regs[p[1]] + int(p[2].lstrip('#'), 16) == msg_va:
                ref = i
                break
    if ref is None:
        raise SystemExit('no se encontró la referencia al mensaje del token')
    # Inicio de la función: primer "sub sp, sp, #N" seguido de "stp x29, x30" hacia atrás
    for j in range(ref, max(0, ref - 400), -1):
        if insns[j].mnemonic == 'sub' and insns[j].op_str.startswith('sp, sp, #') \
                and insns[j + 1].mnemonic == 'stp' and insns[j + 1].op_str.startswith('x29, x30'):
            start = insns[j].address
            break
    else:
        raise SystemExit('no se encontró el inicio de la función')
    # Comprobación: la función debe construir "androidboot.fastbootd=true" (0x6e61 = "an")
    body = [x for x in insns if start <= x.address <= insns[ref].address + 0x100]
    if not any(x.mnemonic == 'mov' and x.op_str.endswith('#0x6e61') for x in body):
        raise SystemExit('la función encontrada no comprueba androidboot.fastbootd=true')
    return start, toff + (start - base)


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    stock, out = sys.argv[1], sys.argv[2]
    hdr, ps, parts = br.parse_boot(br.load(stock))
    cpio = br.lz4_legacy_decompress(parts['ramdisk'])
    ents = br.cpio_parse(cpio)
    rec = next(e for e in ents if e[0] == 'system/bin/recovery')
    binary = bytearray(rec[2])
    va, off = find_check(bytes(binary))
    print(f'función del token en {va:#x} (offset {off:#x}); original: {binary[off:off + 8].hex()}')
    binary[off:off + 8] = PATCH
    # Verificación del parche desensamblando
    md = Cs(CS_ARCH_ARM64, CS_MODE_ARM)
    print('parcheado:', [f'{i.mnemonic} {i.op_str}' for i in md.disasm(bytes(binary[off:off + 8]), va)])
    rec[2] = bytes(binary)
    rec[1] = dict(rec[1], mtime=int(time.time()))

    new_cpio = br.cpio_write(ents)
    parts['ramdisk'] = br.lz4_legacy_compress(new_cpio)
    assert br.lz4_legacy_decompress(parts['ramdisk']) == new_cpio
    img = br.build_boot(hdr, ps, parts)
    _, _, p2 = br.parse_boot(img)
    chk = {e[0]: e for e in br.cpio_parse(br.lz4_legacy_decompress(p2['ramdisk']))}
    assert chk['system/bin/recovery'][2][off:off + 8] == PATCH
    diff = sum(a != b for a, b in zip(chk['system/bin/recovery'][2], rec[2]))
    assert diff == 0

    os.makedirs(out, exist_ok=True)
    ip = os.path.join(out, 'recovery-stock-fastboot.img')
    open(ip, 'wb').write(img)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w', format=tarfile.USTAR_FORMAT) as t:
        ti = tarfile.TarInfo('recovery.img'); ti.size, ti.mode, ti.mtime = len(img), 0o644, int(time.time())
        t.addfile(ti, io.BytesIO(img))
    tar = buf.getvalue()
    tp = os.path.join(out, 'recovery-stock-fastboot.tar.md5')
    open(tp, 'wb').write(tar + f'{hashlib.md5(tar).hexdigest()}  recovery-stock-fastboot.tar\n'.encode())
    print(f'{ip}: sha256={hashlib.sha256(img).hexdigest()}')
    print(f'{tp}: sha256={hashlib.sha256(open(tp, "rb").read()).hexdigest()}')


if __name__ == '__main__':
    main()
