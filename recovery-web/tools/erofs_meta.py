#!/usr/bin/env python3
"""Metadatos de imágenes EROFS (Android): modo, uid, gid, xattrs (SELinux, capabilities).

fsck.erofs extrae contenido y permisos pero no xattrs; sin las etiquetas SELinux
y capabilities una partición reconstruida no arranca. Esta herramienta:

  dump   IMG OUT.json            vuelca los metadatos de cada ruta
  apply  META.json DIR           aplica dueño/permisos/xattrs a un árbol extraído
  verify META.json IMG [--removed LISTA]
                                 compara una imagen reconstruida con el original
"""
import json, os, stat, struct, sys

PREFIX = {1: 'user.', 2: 'system.posix_acl_access', 3: 'system.posix_acl_default',
          4: 'trusted.', 6: 'security.'}


class Erofs:
    def __init__(self, path):
        self.f = open(path, 'rb')
        sb = self.read(1024, 128)
        magic, _, _, bits, _, self.root, _, _, _, self.blocks, self.meta, self.xattr_blk = \
            struct.unpack_from('<IIIBBHQQIIII', sb, 0)
        assert magic == 0xE0F5E1E2, 'no es EROFS'
        self.bs = 1 << bits
        incompat, = struct.unpack_from('<I', sb, 80)
        self.prefixes = {}
        # Prefijos largos de xattr (feature incompat 0x40): no usados por Android normalmente
        if incompat & 0x40:
            raise NotImplementedError('long xattr prefixes no soportados')

    def read(self, off, n):
        self.f.seek(off)
        return self.f.read(n)

    def inode(self, nid):
        off = self.meta * self.bs + nid * 32
        b = self.read(off, 64)
        fmt, icount, mode = struct.unpack_from('<HHH', b, 0)
        ext = fmt & 1
        layout = (fmt >> 1) & 7
        if ext:
            size, = struct.unpack_from('<Q', b, 8)
            raw, _, uid, gid = struct.unpack_from('<IIII', b, 16)
            isz = 64
        else:
            size, = struct.unpack_from('<I', b, 8)
            raw, = struct.unpack_from('<I', b, 16)
            uid, gid = struct.unpack_from('<HH', b, 24)
            isz = 32
        xsz = 12 + (icount - 1) * 4 if icount else 0
        return dict(off=off, isz=isz, xsz=xsz, mode=mode, size=size, raw=raw,
                    uid=uid, gid=gid, layout=layout, icount=icount)

    def xattrs(self, ino):
        out = {}
        if not ino['icount']:
            return out
        base = ino['off'] + ino['isz']
        hdr = self.read(base, ino['xsz'])
        shared_cnt = hdr[4]
        entries = []
        for i in range(shared_cnt):
            sid, = struct.unpack_from('<I', hdr, 12 + 4 * i)
            entries.append(self.xattr_blk * self.bs + sid * 4)
        p = 12 + 4 * shared_cnt
        while p + 4 <= len(hdr):
            nl, idx, vs = struct.unpack_from('<BBH', hdr, p)
            if nl == 0 and idx == 0 and vs == 0:
                break
            name = hdr[p + 4:p + 4 + nl].decode()
            val = hdr[p + 4 + nl:p + 4 + nl + vs]
            out[PREFIX.get(idx, f'?{idx}.') + name] = val.hex()
            p += (4 + nl + vs + 3) & ~3
        for o in entries:
            e = self.read(o, 4)
            nl, idx, vs = struct.unpack('<BBH', e)
            d = self.read(o + 4, nl + vs)
            out[PREFIX.get(idx, f'?{idx}.') + d[:nl].decode()] = d[nl:].hex()
        return out

    def data(self, ino):
        """Datos de un inodo sin comprimir (layout 0 plano, 2 plano con cola inline)."""
        size, lay = ino['size'], ino['layout']
        if lay == 0:
            return self.read(ino['raw'] * self.bs, size)
        if lay == 2:
            nblk = size // self.bs
            head = self.read(ino['raw'] * self.bs, nblk * self.bs) if nblk else b''
            tail_off = ino['off'] + ino['isz'] + ino['xsz']
            return head + self.read(tail_off, size - nblk * self.bs)
        raise NotImplementedError(f'layout {lay} (comprimido) en directorio/enlace')

    def readdir(self, ino):
        d = self.data(ino)
        out = []
        for blk in range(0, len(d), self.bs):
            b = d[blk:blk + self.bs]
            nid, nameoff, ft = struct.unpack_from('<QHB', b, 0)
            n = nameoff // 12
            ents = [struct.unpack_from('<QHB', b, 12 * i) for i in range(n)]
            for i, (nid, no, ft) in enumerate(ents):
                end = ents[i + 1][1] if i + 1 < n else len(b)
                name = b[no:end].split(b'\0', 1)[0].decode('utf-8', 'surrogateescape')
                if name not in ('.', '..'):
                    out.append((name, nid))
        return out

    def walk(self):
        stack = [('/', self.root)]
        while stack:
            path, nid = stack.pop()
            ino = self.inode(nid)
            m = ino['mode']
            ent = dict(mode=m, uid=ino['uid'], gid=ino['gid'], x=self.xattrs(ino))
            if stat.S_ISLNK(m):
                ent['link'] = self.data(ino).decode('utf-8', 'surrogateescape')
            elif stat.S_ISREG(m):
                ent['size'] = ino['size']
            yield path, ent
            if stat.S_ISDIR(m):
                for name, cnid in self.readdir(ino):
                    stack.append((path.rstrip('/') + '/' + name, cnid))


def cmd_dump(img, out):
    meta = dict(Erofs(img).walk())
    json.dump(meta, open(out, 'w'))
    labelled = sum(1 for v in meta.values() if 'security.selinux' in v['x'])
    caps = sum(1 for v in meta.values() if 'security.capability' in v['x'])
    print(f'{len(meta)} rutas, {labelled} con etiqueta SELinux, {caps} con capabilities')


def cmd_apply(meta_path, root):
    meta = json.load(open(meta_path))
    done = missing = 0
    # Hijos antes que padres: los chmod de directorios restrictivos al final
    for path in sorted(meta, key=lambda p: -p.count('/')):
        e = meta[path]
        fs = os.path.join(root, path.lstrip('/'))
        if not os.path.lexists(fs):
            missing += 1
            continue
        os.lchown(fs, e['uid'], e['gid'])
        if not stat.S_ISLNK(e['mode']):
            os.chmod(fs, stat.S_IMODE(e['mode']))
        for k, v in e['x'].items():
            os.setxattr(fs, k, bytes.fromhex(v), follow_symlinks=False)
        done += 1
    print(f'aplicado a {done} rutas ({missing} ausentes: eliminadas a propósito)')


def cmd_verify(meta_path, img, removed=None):
    orig = json.load(open(meta_path))
    new = dict(Erofs(img).walk())
    rm = [r.rstrip('/') for r in (open(removed).read().split() if removed else [])]
    is_removed = lambda p: any(p == r or p.startswith(r + '/') for r in rm)
    errors = 0
    for p, e in orig.items():
        if is_removed(p):
            if p in new:
                print('NO ELIMINADO', p); errors += 1
            continue
        n = new.get(p)
        if n is None:
            print('FALTA', p); errors += 1; continue
        for k in ('mode', 'uid', 'gid', 'x', 'link', 'size'):
            a, b = e.get(k), n.get(k)
            # Linux ignora los permisos de los symlinks (lstat siempre da 0777).
            if k == 'mode' and stat.S_ISLNK(a) and stat.S_ISLNK(b):
                continue
            if a != b:
                print('DIFERENTE', k, p, e.get(k), '->', n.get(k)); errors += 1
    for p in new:
        if p not in orig:
            print('NUEVO', p); errors += 1
    print(f'{len(new)} rutas verificadas, {errors} diferencias')
    return errors == 0


if __name__ == '__main__':
    a = sys.argv[1:]
    if a[:1] == ['dump']:
        cmd_dump(a[1], a[2])
    elif a[:1] == ['apply']:
        cmd_apply(a[1], a[2])
    elif a[:1] == ['verify']:
        ok = cmd_verify(a[1], a[2], a[4] if len(a) > 4 and a[3] == '--removed' else None)
        sys.exit(0 if ok else 1)
    else:
        sys.exit(__doc__)
