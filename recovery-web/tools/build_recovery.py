#!/usr/bin/env python3
"""Reempaqueta un recovery.img stock de Samsung (boot image v2, ramdisk LZ4 legacy)
sustituyendo /system/bin/recovery por rweb y conservando el original como
/system/bin/recovery.samsung. Genera recovery.img y recovery.tar.md5 (Odin).

Uso: build_recovery.py STOCK_recovery.img(.lz4) rweb-arm64 OUT_DIR [--sepolicy FILE]
Requiere: pip install lz4
"""
import argparse, hashlib, io, os, struct, sys, tarfile, time
import lz4.block, lz4.frame

PAGE_DEFAULT = 4096
LEGACY_MAGIC = b'\x02\x21\x4c\x18'
LEGACY_BLOCK = 8 << 20


def load(path):
    d = open(path, 'rb').read()
    if d[:4] == b'\x04\x22\x4d\x18':
        d = lz4.frame.decompress(d)
    return d


def lz4_legacy_decompress(d):
    assert d[:4] == LEGACY_MAGIC, 'ramdisk no es LZ4 legacy'
    out, i = bytearray(), 4
    while i + 4 <= len(d):
        n, = struct.unpack_from('<I', d, i)
        i += 4
        if n == 0x184C2102:
            continue
        if n == 0 or i + n > len(d):
            break
        out += lz4.block.decompress(d[i:i + n], uncompressed_size=LEGACY_BLOCK)
        i += n
    return bytes(out)


def lz4_legacy_compress(d):
    out = bytearray(LEGACY_MAGIC)
    for i in range(0, len(d), LEGACY_BLOCK):
        c = lz4.block.compress(d[i:i + LEGACY_BLOCK], mode='high_compression',
                               compression=12, store_size=False)
        out += struct.pack('<I', len(c)) + c
    return bytes(out)


# ---- cpio newc ----
FIELDS = ['ino', 'mode', 'uid', 'gid', 'nlink', 'mtime', 'filesize', 'devmajor',
          'devminor', 'rdevmajor', 'rdevminor', 'namesize', 'check']


def cpio_parse(d):
    ents, i = [], 0
    while True:
        if d[i:i + 6] != b'070701':
            raise ValueError(f'cpio corrupto en {i}')
        h = {f: int(d[i + 6 + 8 * k:i + 14 + 8 * k], 16) for k, f in enumerate(FIELDS)}
        name = d[i + 110:i + 110 + h['namesize'] - 1].decode()
        i = (i + 110 + h['namesize'] + 3) & ~3
        body = d[i:i + h['filesize']]
        i = (i + h['filesize'] + 3) & ~3
        if name == 'TRAILER!!!':
            return ents
        ents.append([name, h, body])


def cpio_write(ents):
    out = bytearray()
    ents = ents + [['TRAILER!!!', {f: 0 for f in FIELDS} | {'nlink': 1}, b'']]
    for ino, (name, h, body) in enumerate(ents, 300000):
        nb = name.encode() + b'\0'
        h = dict(h, namesize=len(nb), filesize=len(body), check=0)
        if name != 'TRAILER!!!':
            h['ino'] = ino
        out += b'070701' + b''.join(b'%08X' % h[f] for f in FIELDS) + nb
        out += b'\0' * ((-len(out)) % 4) + body
        out += b'\0' * ((-len(out)) % 4)
    out += b'\0' * ((-len(out)) % 512)
    return bytes(out)


# ---- boot image v2 ----
def parse_boot(d):
    assert d[:8] == b'ANDROID!', 'no es un boot image'
    ks, ka, rs, ra, ss, sa, ta, ps, hv, osv = struct.unpack_from('<10I', d, 8)
    assert hv == 2, f'header v{hv} no soportado (se esperaba v2)'
    rdo, = struct.unpack_from('<I', d, 1632)
    dtbs, = struct.unpack_from('<I', d, 1648)
    pg = lambda n: (n + ps - 1) // ps * ps
    off, parts = ps, {}
    for n, s in [('kernel', ks), ('ramdisk', rs), ('second', ss), ('recovery_dtbo', rdo), ('dtb', dtbs)]:
        parts[n] = d[off:off + s]
        off += pg(s)
    return d[:1660], ps, parts


def build_boot(hdr, ps, parts):
    hdr = bytearray(hdr)
    struct.pack_into('<I', hdr, 8, len(parts['kernel']))
    struct.pack_into('<I', hdr, 16, len(parts['ramdisk']))
    struct.pack_into('<I', hdr, 24, len(parts['second']))
    struct.pack_into('<I', hdr, 1632, len(parts['recovery_dtbo']))
    pg = lambda n: (n + ps - 1) // ps * ps
    body = bytearray()
    sha = hashlib.sha1()
    off = ps
    for n in ['kernel', 'ramdisk', 'second', 'recovery_dtbo', 'dtb']:
        b = parts[n]
        sha.update(b)
        sha.update(struct.pack('<I', len(b)))
        body += b + b'\0' * (pg(len(b)) - len(b))
    dtbo_off = ps + sum(pg(len(parts[n])) for n in ['kernel', 'ramdisk', 'second'])
    struct.pack_into('<Q', hdr, 1636, dtbo_off if parts['recovery_dtbo'] else 0)
    struct.pack_into('<I', hdr, 1648, len(parts['dtb']))
    hdr[576:608] = sha.digest().ljust(32, b'\0')
    img = bytes(hdr).ljust(ps, b'\0') + bytes(body)
    return img + b'SEANDROIDENFORCE'


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('stock')
    ap.add_argument('rweb')
    ap.add_argument('out')
    ap.add_argument('--sepolicy', help='sepolicy modificada para sustituir /sepolicy')
    ap.add_argument('--max-size', type=int, default=100663296, help='tamaño de la partición recovery')
    a = ap.parse_args()

    stock = load(a.stock)
    hdr, ps, parts = parse_boot(stock)
    cpio = lz4_legacy_decompress(parts['ramdisk'])
    ents = cpio_parse(cpio)
    byname = {e[0]: e for e in ents}

    rec = byname.get('system/bin/recovery')
    if rec is None:
        sys.exit('no se encontró system/bin/recovery en el ramdisk')
    if b'rweb' in rec[2][:4096] or 'system/bin/recovery.samsung' in byname:
        sys.exit('este recovery ya contiene rweb; usa el stock original')

    rweb = open(a.rweb, 'rb').read()
    assert rweb[:4] == b'\x7fELF' and rweb[18] == 0xB7, 'rweb debe ser ELF arm64'
    stock_entry = ['system/bin/recovery.samsung', dict(rec[1]), rec[2]]
    rec[2] = rweb
    rec[1] = dict(rec[1], mode=0o100755, mtime=int(time.time()))
    ents.insert(ents.index(rec) + 1, stock_entry)

    if a.sepolicy:
        sp = byname.get('sepolicy')
        if sp is None:
            sys.exit('no hay /sepolicy en el ramdisk')
        sp[2] = open(a.sepolicy, 'rb').read()

    new_cpio = cpio_write(ents)
    assert len(cpio_parse(new_cpio)) == len(ents), 'verificación cpio fallida'
    parts['ramdisk'] = lz4_legacy_compress(new_cpio)
    assert lz4_legacy_decompress(parts['ramdisk']) == new_cpio, 'verificación LZ4 fallida'

    img = build_boot(hdr, ps, parts)
    if len(img) > a.max_size:
        sys.exit(f'la imagen ({len(img)}) supera la partición ({a.max_size})')
    # Verificación final: re-parsear la imagen generada
    _, _, p2 = parse_boot(img)
    assert p2['kernel'] == parts['kernel'] and p2['dtb'] == parts['dtb']
    assert cpio_parse(lz4_legacy_decompress(p2['ramdisk']))[0][0] == ents[0][0]

    os.makedirs(a.out, exist_ok=True)
    ip = os.path.join(a.out, 'recovery.img')
    open(ip, 'wb').write(img)

    # recovery.tar.md5 para Odin (pestaña AP)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w', format=tarfile.USTAR_FORMAT) as t:
        ti = tarfile.TarInfo('recovery.img')
        ti.size, ti.mode, ti.mtime = len(img), 0o644, int(time.time())
        t.addfile(ti, io.BytesIO(img))
    tar = buf.getvalue()
    md5 = hashlib.md5(tar).hexdigest()
    tp = os.path.join(a.out, 'recovery.tar.md5')
    open(tp, 'wb').write(tar + f'{md5}  recovery.tar\n'.encode())

    print(f'ramdisk: {len(cpio)} -> {len(new_cpio)} bytes ({len(parts["ramdisk"])} comprimido)')
    print(f'{ip}: {len(img)} bytes  sha256={hashlib.sha256(img).hexdigest()}')
    print(f'{tp}: sha256={hashlib.sha256(open(tp,"rb").read()).hexdigest()}')


if __name__ == '__main__':
    main()
