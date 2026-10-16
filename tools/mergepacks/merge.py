"""Builds the JRAFT merge resource packs: pack/packs/jraft-grass.zip and jraft-leaves.zip.

Several grass and leaf packs in the pack replace the same blockstate files, so only the
top one of each would show. The merge packs sit above them and replace those files with
ones that use every pack's models together. They contain only files written here, plus
copies of MIT-licensed Better Leaves textures and models: the All Rights Reserved packs'
models and textures are only referenced by name, never copied, and still come from their
own downloads.

The merged files are built from the exact upstream versions in pack/lock.json, which
pack.json pins. Before unpinning or updating one of them, run this again and check what
it reports:

    python tools/mergepacks/merge.py --vanilla <path to the 26.3 client jar>

It downloads the locked resource packs into a cache folder (checking each sha512), writes
the readable sources to pack/packs-src/ and the zips to pack/packs/, and then checks that
every model and texture the merge packs point at exists and comes from the intended pack
in the pack's real load order. Stdlib only.
"""
import argparse, hashlib, io, json, os, re, sys, urllib.request, zipfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', '..'))
FORMAT = (97, 1)  # Minecraft 26.3 resource pack format
INF = 10 ** 9

# Upstream packs the merges use, by Modrinth slug, at the versions this was written for.
SOURCES = {
    'grassrise': '1.0.3',
    'simple-grass-flowers': '2.0.0',
    'connected-paths': '3.1.1+mc26.2-26.3',
    'os-colorful-grasses': 'Tall-26.3',
    'grass-clovers': '2',
    'simple-foliage': '1.4',
    'bushier-bushes': 'v3.2.0',
    'better-leaves': '9.6',
    'os-colorful-leaves': 'ColorfulLeaves-26.3',
}

VANILLA_LEAVES = ['oak', 'spruce', 'birch', 'jungle', 'acacia', 'dark_oak', 'mangrove', 'cherry',
                  'azalea', 'flowering_azalea', 'pale_oak']


# ---- reading packs -------------------------------------------------------------------

def bound(v, low):
    if isinstance(v, list):
        return (int(v[0]), int(v[1]) if len(v) > 1 else (0 if low else INF))
    if isinstance(v, float):
        a, b = repr(v).split('.')
        return (int(a), int(b))
    return (int(v), 0 if low else INF)


def overlay_applies(e):
    if 'min_format' in e or 'max_format' in e:
        return bound(e.get('min_format', 0), True) <= FORMAT <= bound(e.get('max_format', INF), False)
    f = e.get('formats')
    if isinstance(f, dict):
        return bound(f.get('min_inclusive', 0), True) <= FORMAT <= bound(f.get('max_inclusive', INF), False)
    if isinstance(f, list):
        return bound(f[0], True) <= FORMAT <= bound(f[1], False)
    return isinstance(f, int) and f == FORMAT[0]


def loose_json(b):
    t = b.decode('utf-8-sig', 'replace')
    try:
        return json.loads(t)
    except ValueError:
        t = re.sub(r'//[^\n]*', '', t)
        return json.loads(re.sub(r',\s*([}\]])', r'\1', t))


class Pack:
    """A resource pack's files as the game sees them on 26.3: base assets plus the
    overlays whose format range covers 26.3, later overlays winning."""

    def __init__(self, name, data):
        self.name = name
        self.z = zipfile.ZipFile(io.BytesIO(data))
        names = self.z.namelist()
        meta = loose_json(self.z.read('pack.mcmeta')) if 'pack.mcmeta' in names else {}
        overlays = [e['directory'] for e in meta.get('overlays', {}).get('entries', []) if overlay_applies(e)]
        self.files = {n: n for n in names if n.startswith('assets/') and not n.endswith('/')}
        for d in overlays:
            for n in names:
                if n.startswith(d + '/assets/') and not n.endswith('/'):
                    self.files[n[len(d) + 1:]] = n

    def read(self, p):
        return self.z.read(self.files[p])

    def json(self, p):
        return loose_json(self.read(p))


def fetch_packs(lock, cache):
    """Every resource pack in the lock, in load order (bottom first), keyed by slug."""
    os.makedirs(cache, exist_ok=True)
    by_path = {f['path']: f for f in lock['files']}
    out = []
    for entry in lock['resourcePacks']:
        if not entry.startswith('file/'):
            continue
        f = by_path['resourcepacks/' + entry[5:]]
        if f.get('local'):
            continue  # one of ours
        slug = f.get('comment', '').split(' ', 1)[0]
        dest = os.path.join(cache, f['sha512'][:32] + '.zip')
        if not os.path.exists(dest):
            req = urllib.request.Request(f['url'], headers={'User-Agent': 'Lonezsi/jraft-launcher mergepacks'})
            data = urllib.request.urlopen(req, timeout=120).read()
            if hashlib.sha512(data).hexdigest() != f['sha512']:
                sys.exit(f'checksum mismatch for {f["url"]}')
            open(dest, 'wb').write(data)
        out.append((slug, f, Pack(entry[5:], open(dest, 'rb').read())))
    return out


# ---- the merges ----------------------------------------------------------------------

def check_version(lockfile, slug):
    have = lockfile.get('comment', '').split(' ', 1)[1] if ' ' in lockfile.get('comment', '') else ''
    if have != SOURCES[slug]:
        sys.exit(f'{slug} is {have!r} in the lock but the merge was written for {SOURCES[slug]!r}. '
                 f'Check the merged files against the new version, then update SOURCES.')


def grass_pack(src):
    """Blockstates that combine every grass pack's models."""
    G = lambda slug, block: src[slug].json(f'assets/minecraft/blockstates/{block}.json')
    out = {}

    # Grass blocks: Simple Grass Flowers picks the whole block (plain, clovers, small flowers,
    # rocks); Grassrise's blades go on top as a separate multipart layer, so both show. Snowy
    # blocks use Connected Paths' snow model, which lets paths connect under snow too.
    sgf, rise, cp = G('simple-grass-flowers', 'grass_block'), G('grassrise', 'grass_block'), G('connected-paths', 'grass_block')
    blades = next(p['apply'] for p in rise['multipart']
                  if p['when'] == {'snowy': 'false'} and any('grassrise:' in a['model'] for a in p['apply']))
    out['grass_block'] = {'multipart': [
        {'when': {'snowy': 'false'}, 'apply': sgf['variants']['snowy=false']},
        {'when': {'snowy': 'false'}, 'apply': blades},
        {'when': {'snowy': 'true'}, 'apply': cp['variants']['snowy=true']},
    ]}

    # Podzol and mycelium: Simple Grass Flowers' decorated blocks, Connected Paths' snow.
    for b in ('podzol', 'mycelium'):
        out[b] = {'variants': {
            'snowy=false': G('simple-grass-flowers', b)['variants']['snowy=false'],
            'snowy=true': G('connected-paths', b)['variants']['snowy=true'],
        }}

    # Short grass: one pool. Os' fluffy colorful grass and Simple Foliage's variants about
    # equally, clover patches rarer. Grass Clovers' own plain vanilla entry is dropped,
    # the other two packs cover plain grass.
    osg = G('os-colorful-grasses', 'short_grass')['variants']['']
    sf = G('simple-foliage', 'short_grass')['variants']['']
    clov = [v for v in G('grass-clovers', 'short_grass')['variants'][''] if 'clover' in v['model']]
    out['short_grass'] = {'variants': {'': [dict(v, weight=4) for v in osg] + sf + [dict(v, weight=2) for v in clov]}}

    # Tall grass is two blocks, but the game seeds both halves' random pick with the lower
    # block's position, so the same index wins on both halves when both lists have the
    # same weights in the same order. Os' has one bottom for four tops, so its bottom is
    # repeated to pair with each top; Simple Foliage's halves already line up.
    o_lo = G('os-colorful-grasses', 'tall_grass')['variants']['half=lower']
    o_lo = o_lo[0] if isinstance(o_lo, list) else o_lo
    o_hi = G('os-colorful-grasses', 'tall_grass')['variants']['half=upper']
    s = G('simple-foliage', 'tall_grass')['variants']
    lower = [dict(o_lo, weight=2) for _ in o_hi] + s['half=lower']
    upper = [dict(v, weight=2) for v in o_hi] + s['half=upper']
    assert [v.get('weight', 1) for v in lower] == [v.get('weight', 1) for v in upper]
    out['tall_grass'] = {'variants': {'half=lower': lower, 'half=upper': upper}}

    # Bushes: Bushier Bushes' fuller 3D bush half the time, Simple Foliage's variants the rest.
    out['bush'] = {'variants': {'': G('simple-foliage', 'bush')['variants'][''] + [{'model': 'minecraft:block/bush', 'weight': 8}]}}

    # Red shrubs: Os' blue, pink and yellow ones plus Bushier Bushes' 3D red shrub, which
    # takes the place of Os' flat red one (both use the model name block/red_shrub, so the
    # pack order puts Bushier Bushes above Os').
    out['red_shrub'] = G('os-colorful-grasses', 'red_shrub')
    return {f'assets/minecraft/blockstates/{b}.json': j for b, j in out.items()}


def leaves_pack(src):
    """Better Leaves' bushy shape with Os' Colorful Leaves' textures.

    Better Leaves draws a leaf block as a cube showing the middle of a 32x32 round leaf
    texture, plus two crossed planes showing all of it that stick out past the block. Os'
    replaces those textures with ordinary 16x16 tiled leaves, which on the planes come out
    as square sheets. Here the cube uses Os' texture across its whole face and the planes
    keep Better Leaves' round texture, copied under jraft: so Os' can still own the vanilla
    name. Better Leaves is MIT licensed.
    """
    bl, ol = src['better-leaves'], src['os-colorful-leaves']
    out = {}
    for tint in ('', '_notint'):
        for n in range(1, 5):
            m = bl.json(f'assets/betterleaves/models/block/leaves{tint}{n}.json')
            m.pop('credit', None)
            cube, *planes = m['elements']
            assert cube['from'] == [0, 0, 0] and cube['to'] == [16, 16, 16] and len(planes) == 2
            for face in cube['faces'].values():
                face['uv'] = [0, 0, 16, 16]
            for p in planes:
                for face in p['faces'].values():
                    face['texture'] = '#fluff'
            m.pop('texture_size', None)
            m['credit'] = "Shape from Motschen's Better Leaves (MIT)"
            out[f'assets/jraft/models/block/bushy_leaves{tint}{n}.json'] = m
    for leaf in VANILLA_LEAVES:
        tex = f'assets/minecraft/textures/block/{leaf}_leaves.png'
        if tex not in ol.files:
            continue  # Os' leaves this one alone, so Better Leaves' own model is right
        out[f'assets/jraft/textures/block/better_leaves/{leaf}_leaves.png'] = bl.read(tex)
        for n in range(1, 5):
            parent = bl.json(f'assets/minecraft/models/block/{leaf}_leaves{n}.json')['parent']
            tint = '_notint' if 'notint' in parent else ''
            out[f'assets/minecraft/models/block/{leaf}_leaves{n}.json'] = {
                'parent': f'jraft:block/bushy_leaves{tint}{n}',
                'textures': {'all': f'minecraft:block/{leaf}_leaves', 'fluff': f'jraft:block/better_leaves/{leaf}_leaves'},
            }
    out['LICENSE-BetterLeaves.txt'] = bl.z.read('LICENSE')
    return out


def mcmeta(desc):
    return {'pack': {'description': desc, 'min_format': 97, 'max_format': 97}}


# ---- checking ------------------------------------------------------------------------

def rl(s, kind):
    ns, p = s.split(':', 1) if ':' in s else ('minecraft', s)
    return f'assets/{ns}/{kind}/{p}' + ('.json' if kind == 'models' else '.png')


def models_in(j):
    found = []
    def walk(v):
        if isinstance(v, dict):
            if isinstance(v.get('model'), str):
                found.append(v['model'])
            for x in v.values():
                walk(x)
        elif isinstance(v, list):
            for x in v:
                walk(x)
    walk(j)
    return found


def validate(order, ours, vanilla, expect):
    """order: (name, files-dict, reader) bottom to top, ours included at its place.
    Resolves every model, parent and texture the merge packs reach and reports any that
    is missing, or that resolves to a different pack than expect says it should."""
    def provider(path):
        for name, files, read in reversed(order):
            if path in files:
                return name, read
        if vanilla and path in vanilla[0]:
            return 'vanilla', lambda p: vanilla[1].read(p)
        return None, None

    problems, seen = [], set()
    def model(m, owner):
        path = rl(m, 'models')
        if path in seen:
            return
        seen.add(path)
        name, read = provider(path)
        if name is None:
            if vanilla or not m.startswith(('minecraft:', 'block/', 'item/')):
                problems.append(f'missing model {m} (used by {owner})')
            return
        for pre, want in expect.items():
            if m.split(':')[-1].startswith(pre) and not name.startswith(want):
                problems.append(f'model {m} resolves to {name}, expected {want}')
        j = loose_json(read(path))
        if j.get('parent') and not j['parent'].startswith('builtin/'):
            model(j['parent'], m)
        for t in (j.get('textures') or {}).values():
            if not t.startswith('#'):
                tp = rl(t, 'textures')
                if provider(tp)[0] is None and (vanilla or not t.startswith(('minecraft:', 'block/'))):
                    problems.append(f'missing texture {t} (model {m})')
    for name, files, read in order:
        if name not in ours:
            continue
        for p in files:
            if p.endswith('.json') and '/blockstates/' in p:
                for m in models_in(loose_json(read(p))):
                    model(m, p)
            elif p.endswith('.json') and '/models/' in p:
                ns = p.split('/')[1]
                model(ns + ':' + p.split('/models/', 1)[1][:-5], p)
    return problems


# ---- output --------------------------------------------------------------------------

def write_pack(name, files, out):
    src = os.path.join(out, 'packs-src', name)
    for p, data in files.items():
        dest = os.path.join(src, *p.split('/'))
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        if not isinstance(data, (bytes, bytearray)):
            data = (json.dumps(data, indent=2, ensure_ascii=False) + '\n').encode()
            files[p] = data
        open(dest, 'wb').write(data)
    os.makedirs(os.path.join(out, 'packs'), exist_ok=True)
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, 'w') as z:
        for p in sorted(files):
            zi = zipfile.ZipInfo(p, date_time=(1980, 1, 1, 0, 0, 0))
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.external_attr = 0o644 << 16
            z.writestr(zi, files[p], compresslevel=9)
    zpath = os.path.join(out, 'packs', name + '.zip')
    open(zpath, 'wb').write(buf.getvalue())
    print(f'wrote {os.path.relpath(zpath, ROOT)}: {len(files)} files, {len(buf.getvalue()) // 1024} KB')


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--dir', default=os.path.join(ROOT, 'pack'))
    ap.add_argument('--cache', default=os.path.join(ROOT, '.cache', 'mergepacks'))
    ap.add_argument('--vanilla', help='26.3 client jar, to also check vanilla models exist')
    a = ap.parse_args()

    lock = json.load(open(os.path.join(a.dir, 'lock.json'), encoding='utf-8'))
    packs = fetch_packs(lock, a.cache)
    src = {}
    for slug, f, p in packs:
        if slug in SOURCES:
            check_version(f, slug)
            src[slug] = p
    missing = set(SOURCES) - set(src)
    if missing:
        sys.exit(f'not enabled in the pack: {sorted(missing)}')

    icon = open(os.path.join(ROOT, 'assets', 'icon.png'), 'rb').read()
    grass = grass_pack(src)
    grass['pack.mcmeta'] = mcmeta('§dJRAFT§7 grass: Grassrise, Simple Grass Flowers, Os\' grasses, Simple Foliage, Clovers, Bushier Bushes, Connected Paths together')
    grass['pack.png'] = icon
    leaves = leaves_pack(src)
    leaves['pack.mcmeta'] = mcmeta('§dJRAFT§7 leaves: Better Leaves\' bushy shape with Os\' Colorful Leaves')
    leaves['pack.png'] = icon

    # Where the merge packs sit in the real load order (the enabled list in pack.json).
    enabled = json.load(open(os.path.join(a.dir, 'pack.json'), encoding='utf-8'))['enabledResourcepacks']
    order = []
    mine = {'jraft-grass': grass, 'jraft-leaves': leaves}
    by_slug = {slug: p for slug, f, p in packs}
    for e in enabled:
        if e in mine:
            fs = {k: k for k in mine[e]}
            order.append((e, fs, (lambda d: lambda p: d[p] if isinstance(d[p], bytes) else json.dumps(d[p]).encode())(mine[e])))
        elif e in by_slug:
            p = by_slug[e]
            order.append((p.name, p.files, p.read))
    if not all(e in enabled for e in mine):
        sys.exit('add jraft-grass and jraft-leaves to enabledResourcepacks in pack.json first')

    vanilla = None
    if a.vanilla:
        vz = zipfile.ZipFile(a.vanilla)
        vanilla = (set(vz.namelist()), vz)
    # Model name prefixes and the pack each must come from in the final order.
    expect = {
        'block/grass_block/': 'Simple Grass Flowers', 'block/podzol/': 'Simple Grass Flowers',
        'block/mycelium/': 'Simple Grass Flowers', 'block/grass_blades': 'Grassrise',
        'block/fake_': 'Connected-Paths', 'block/clover_': 'Grass Clovers',
        'block/short/': 'Simple Foliage', 'block/tall/': 'Simple Foliage', 'block/bush/': 'Simple Foliage',
        'block/bushy_leaves': 'jraft-leaves',
    }
    expect.update({f'block/{leaf}_leaves': 'jraft-leaves' for leaf in VANILLA_LEAVES})
    problems = validate(order, set(mine), vanilla, expect)
    # Models that two packs both name, where the pack order decides the winner on purpose.
    for m, want in [('block/red_shrub', 'Bushier'), ('block/bush', 'Bushier')]:
        for name, files, read in reversed(order):
            if rl(m, 'models') in files:
                if want not in name:
                    problems.append(f'{m} comes from {name}, expected {want}: check the pack order')
                break
    if problems:
        print('\n'.join('PROBLEM: ' + p for p in problems))
        sys.exit(1)
    print('checked: every merged model and texture resolves to the intended pack' + (' (and vanilla)' if vanilla else ''))

    write_pack('jraft-grass', grass, a.dir)
    write_pack('jraft-leaves', leaves, a.dir)


if __name__ == '__main__':
    main()
