#!/usr/bin/env python3
"""Extrae super.img (raw, con huecos) de un ZIP de firmware Samsung remoto sin
descargarlo entero: lee el directorio central con peticiones Range, localiza el
AP_*.tar.md5 y lo procesa en streaming (inflate -> tar -> LZ4 -> sparse -> raw).

Uso: fw_super.py URL_FILE OUT.raw [--probe]
"""
import struct, subprocess, sys, time, zlib
import lz4.frame


def http_range(url, a, b):
    return subprocess.run(['curl', '-sSfL', '--retry', '5', '-r', f'{a}-{b}', url],
                          check=True, capture_output=True).stdout


def find_ap(url):
    hdr = subprocess.run(['curl', '-sSfLI', '--retry', '5', '-r', '0-0', url], check=True,
                         capture_output=True, text=True).stdout
    size = int([l for l in hdr.splitlines() if l.lower().startswith('content-range')][-1].split('/')[-1])
    tail = http_range(url, max(0, size - 65536), size - 1)
    base = size - len(tail)
    i = tail.rfind(b'PK\x06\x06')
    if i >= 0:
        cdsz, cdoff = struct.unpack_from('<QQ', tail, i + 40)
    else:
        i = tail.rfind(b'PK\x05\x06')
        cdsz, cdoff = struct.unpack_from('<II', tail, i + 12)
    p = cdoff - base
    while tail[p:p + 4] == b'PK\x01\x02':
        meth, = struct.unpack_from('<H', tail, p + 10)
        cs, us = struct.unpack_from('<II', tail, p + 20)
        nl, el, cl = struct.unpack_from('<HHH', tail, p + 28)
        lho, = struct.unpack_from('<I', tail, p + 42)
        name = tail[p + 46:p + 46 + nl].decode()
        ex = tail[p + 46 + nl:p + 46 + nl + el]
        q = 0
        while q < len(ex):
            hid, hs = struct.unpack_from('<HH', ex, q)
            if hid == 1:
                v = list(struct.unpack_from('<' + 'Q' * (hs // 8), ex, q + 4))
                if us == 0xFFFFFFFF: us = v.pop(0)
                if cs == 0xFFFFFFFF: cs = v.pop(0)
                if lho == 0xFFFFFFFF: lho = v.pop(0)
            q += 4 + hs
        if name.startswith('AP_') and name.endswith('.tar.md5'):
            lh = http_range(url, lho, lho + 29)
            lnl, lel = struct.unpack_from('<HH', lh, 26)
            return name, meth, lho + 30 + lnl + lel, cs
        p += 46 + nl + el + cl
    sys.exit('no se encontró AP_*.tar.md5 en el ZIP')


class Reader:
    def __init__(self, gen):
        self.g, self.b = gen, bytearray()

    def read(self, n):
        while len(self.b) < n:
            try:
                self.b += next(self.g)
            except StopIteration:
                break
        r = bytes(self.b[:n]); del self.b[:n]
        return r


def main():
    url = open(sys.argv[1]).read().strip()
    out_path = sys.argv[2]
    name, meth, off, csize = find_ap(url)
    print(f'AP: {name} (método {meth}, {csize} bytes comprimidos, offset {off})', flush=True)
    if meth not in (0, 8):
        sys.exit(f'método de compresión ZIP {meth} no soportado')
    proc = subprocess.Popen(['curl', '-sSfL', '--retry', '5', '-r', f'{off}-{off + csize - 1}', url],
                            stdout=subprocess.PIPE)
    z = zlib.decompressobj(-15) if meth == 8 else None

    def zip_stream():
        while True:
            c = proc.stdout.read(1 << 20)
            if not c:
                return
            yield z.decompress(c) if z else c

    tar = Reader(zip_stream())
    while True:
        h = tar.read(512)
        if len(h) < 512 or h == b'\0' * 512:
            sys.exit('super.img.lz4 no encontrado en el AP')
        ename = h[:100].rstrip(b'\0').decode()
        size = int(h[124:136].rstrip(b'\0 ') or b'0', 8)
        print('entrada', ename, size, flush=True)
        if ename == 'super.img.lz4':
            break
        if '--probe' in sys.argv:
            proc.kill(); return
        left = size + (-size) % 512
        while left:
            left -= len(tar.read(min(left, 1 << 22)))

    def lz4_stream(remaining):
        d = lz4.frame.LZ4FrameDecompressor()
        while remaining:
            c = tar.read(min(remaining, 1 << 22))
            if not c:
                break
            remaining -= len(c)
            o = d.decompress(c)
            if o:
                yield o

    raw = Reader(lz4_stream(size))
    magic, _, _, fhs, chs, bs, nblk, nchunk, _ = struct.unpack('<IHHHHIIII', raw.read(28))
    assert magic == 0xED26FF3A, 'super no es sparse'
    raw.read(fhs - 28)
    print(f'sparse: {nblk * bs / 2**30:.2f} GiB, {nchunk} chunks', flush=True)
    t0 = time.time()
    with open(out_path, 'wb') as f:
        f.truncate(nblk * bs)
        pos = 0
        for i in range(nchunk):
            ch = raw.read(chs)
            ct, _, sz, _ = struct.unpack('<HHII', ch[:12])
            raw.read(chs - 12)
            n = sz * bs
            if ct == 0xCAC1:
                f.seek(pos); left = n
                while left:
                    b = raw.read(min(left, 1 << 22))
                    if not b:
                        sys.exit('flujo truncado')
                    f.write(b); left -= len(b)
            elif ct == 0xCAC2:
                fill = raw.read(4)
                if fill != b'\0\0\0\0':
                    f.seek(pos)
                    for _ in range(sz): f.write(fill * (bs // 4))
            elif ct == 0xCAC4:
                raw.read(4)
            pos += n
            if i % 20 == 0:
                print(f'chunk {i}/{nchunk} {pos / 2**30:.2f} GiB {time.time() - t0:.0f}s', flush=True)
    proc.kill()
    print('OK', out_path, flush=True)


if __name__ == '__main__':
    main()
