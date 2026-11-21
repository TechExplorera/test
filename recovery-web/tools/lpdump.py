#!/usr/bin/env python3
"""Lee los metadatos LP (particiones dinámicas) de un super.img raw y,
opcionalmente, extrae cada partición lógica.

Uso: lpdump.py super.raw [--extract DIR] [--only nombre,...]
"""
import argparse, json, os, struct, sys

LP_GEOMETRY_MAGIC = 0x616C4467
LP_METADATA_MAGIC = 0x414C5030
SECTOR = 512


def parse(f):
    f.seek(4096)
    g = f.read(4096)
    magic, ssize = struct.unpack_from('<II', g, 0)
    if magic != LP_GEOMETRY_MAGIC:
        sys.exit('no hay geometría LP en offset 4096')
    max_size, slots, lbs = struct.unpack_from('<III', g, 40)
    f.seek(4096 * 3)  # primary metadata slot 0
    h = f.read(max_size)
    (magic, major, minor, hsize) = struct.unpack_from('<IHHI', h, 0)
    if magic != LP_METADATA_MAGIC:
        sys.exit('no hay cabecera de metadatos LP')
    tables_size, = struct.unpack_from('<I', h, 44)
    descs = [struct.unpack_from('<III', h, 80 + 12 * i) for i in range(4)]
    t = h[hsize:hsize + tables_size]
    tbl = lambda d: [t[d[0] + i * d[2]:d[0] + (i + 1) * d[2]] for i in range(d[1])]
    exts = [dict(zip(['sectors', 'type', 'data', 'source'], struct.unpack_from('<QIQI', e))) for e in tbl(descs[1])]
    groups = [dict(name=e[:36].rstrip(b'\0').decode(), flags=struct.unpack_from('<I', e, 36)[0],
                   max=struct.unpack_from('<Q', e, 40)[0]) for e in tbl(descs[2])]
    bdevs = []
    for e in tbl(descs[3]):
        first, align, aoff, size = struct.unpack_from('<QIIQ', e)
        bdevs.append(dict(name=e[24:60].rstrip(b'\0').decode(), first_sector=first, size=size))
    parts = []
    for e in tbl(descs[0]):
        name = e[:36].rstrip(b'\0').decode()
        attr, fe, ne, gi = struct.unpack_from('<IIII', e, 36)
        pe = exts[fe:fe + ne]
        parts.append(dict(name=name, attrs=attr, group=groups[gi]['name'], extents=pe,
                          size=sum(x['sectors'] for x in pe) * SECTOR))
    return dict(version=f'{major}.{minor}', slots=slots, metadata_max=max_size,
                block_devices=bdevs, groups=groups, partitions=parts)


def extract(f, p, out):
    with open(out, 'wb') as o:
        o.truncate(p['size'])
        pos = 0
        for e in p['extents']:
            n = e['sectors'] * SECTOR
            if e['type'] == 0:  # LINEAR
                f.seek(e['data'] * SECTOR)
                o.seek(pos)
                left = n
                while left:
                    b = f.read(min(left, 1 << 22))
                    if not b:
                        raise IOError('super truncado')
                    o.write(b); left -= len(b)
            pos += n


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('super')
    ap.add_argument('--extract')
    ap.add_argument('--only')
    ap.add_argument('--json', action='store_true')
    a = ap.parse_args()
    with open(a.super, 'rb') as f:
        md = parse(f)
        if a.json:
            print(json.dumps(md, indent=1))
        else:
            print(f"LP v{md['version']}, slots {md['slots']}")
            for b in md['block_devices']:
                print(f"bloque {b['name']}: {b['size']/2**30:.2f} GiB")
            for g in md['groups']:
                print(f"grupo {g['name']}: max {g['max']/2**30:.2f} GiB")
            for p in md['partitions']:
                print(f"  {p['name']:20} {p['size']/2**20:9.1f} MiB  grupo={p['group']} extents={len(p['extents'])}")
        if a.extract:
            os.makedirs(a.extract, exist_ok=True)
            only = set(a.only.split(',')) if a.only else None
            for p in md['partitions']:
                if p['size'] and (only is None or p['name'] in only):
                    extract(f, p, os.path.join(a.extract, p['name'] + '.img'))
                    print('extraído', p['name'])


if __name__ == '__main__':
    main()
