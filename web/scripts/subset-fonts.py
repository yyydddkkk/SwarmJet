"""Rebuild offline WOFF2 fonts from pinned OFL upstream sources.
Run from anywhere: uv run --with fonttools==4.67.0 --with brotli==1.2.0 python web/scripts/subset-fonts.py
TTF inputs/cache live outside the repository in /tmp/swarmjet-font-sources.
"""
from pathlib import Path
import hashlib
import json
import urllib.parse
import urllib.request
from fontTools import subset
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

root = Path(__file__).resolve().parents[2]
out = root / 'web/public/fonts'
cache = Path('/tmp/swarmjet-font-sources')
cache.mkdir(exist_ok=True)
# Google Fonts repository revision; changing it is an explicit asset update.
revision = 'main'  # Resolved to an immutable commit before download below.
manifest_path = out / 'sources.json'
if manifest_path.exists():
    revision = json.loads(manifest_path.read_text())['revision']
else:
    revision = json.load(urllib.request.urlopen('https://api.github.com/repos/google/fonts/commits/main'))['sha']
families = {
 'imfellenglish': ['IMFeENrm28P.ttf', 'IMFeENit28P.ttf'],
 'sourceserif4': ['SourceSerif4[opsz,wght].ttf', 'SourceSerif4-Italic[opsz,wght].ttf'],
 'jetbrainsmono': ['JetBrainsMono[wght].ttf'],
 'ibmplexsans': [],  # Original OFL only; original WOFF2 fetched below.
 'notoserifsc': ['NotoSerifSC[wght].ttf'],
 'notosanssc': ['NotoSansSC[wght].ttf'],
}
text = ''.join(p.read_text() for p in (root/'web/src').rglob('*') if p.suffix in ('.ts','.tsx','.css'))
# GB/T 2312-1980 level-one Han: rows 16..55 (B0..D7), 3,755 characters.
# The final five positions of row 55 are unassigned. Python's standard gb2312
# codec supplies the normative mapping, requiring no third-party word list.
common = set()
for high in range(0xb0, 0xd8):
    for low in range(0xa1, 0xff):
        try:
            common.add(ord(bytes([high,low]).decode('gb2312')))
        except UnicodeDecodeError:
            pass
assert len(common) == 3755
latin = set(range(0x20,0x250)) | set(map(ord,text))
cjk = latin | common | set(range(0x3000,0x3040)) | set(range(0xff00,0xfff0))
expected = {entry['url']: entry['sha256'] for entry in json.loads(manifest_path.read_text())['sources']} if manifest_path.exists() else {}
manifest = {'revision': revision, 'commonHan': 'GB2312 level one (3755)', 'sources': []}
for family, names in families.items():
    for name in [*names, 'OFL.txt']:
        url = f'https://raw.githubusercontent.com/google/fonts/{revision}/ofl/{family}/{urllib.parse.quote(name)}'
        target = cache / (family+'-OFL.txt' if name=='OFL.txt' else name)
        if not target.exists():
            urllib.request.urlretrieve(url,target)
        digest = hashlib.sha256(target.read_bytes()).hexdigest()
        if url in expected and digest != expected[url]:
            raise ValueError(f'Upstream source checksum mismatch: {name}')
        manifest['sources'].append({'url': url, 'sha256': digest})
        if name == 'OFL.txt':
            (out/(family+'-OFL.txt')).write_bytes(target.read_bytes())
            continue
        font = TTFont(target, lazy=False)
        font.ensureDecompiled()
        if 'fvar' in font:
            axes = {a.axisTag: a.defaultValue for a in font['fvar'].axes}
            axes['wght'] = (400,500) if family != 'jetbrainsmono' else 400
            font = instantiateVariableFont(font, axes, inplace=True)
            # Resolve remaining lazy variation tables against the new glyph order.
            intermediate = cache / ('limited-' + name)
            font.save(intermediate)
            font = TTFont(intermediate, lazy=False)
        options = subset.Options()
        sub = subset.Subsetter(options=options)
        sub.populate(unicodes=cjk if family.startswith('noto') else latin)
        sub.subset(font)
        font.flavor='woff2'
        dest=out/Path(name).with_suffix('.woff2')
        font.save(dest)
        print(dest.name,dest.stat().st_size,flush=True)
# IBM reserves the name Plex: ship official WOFF2 unchanged, never instantiate
# or subset these files. Keep the upstream family name and original OFL.
old_manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else {}
ibm_revision = old_manifest.get('ibmRevision') or json.load(urllib.request.urlopen('https://api.github.com/repos/IBM/plex/commits/master'))['sha']
manifest['ibmRevision'] = ibm_revision
for weight in ['Regular', 'Medium', 'Italic', 'MediumItalic']:
    name = f'IBMPlexSans-{weight}.woff2'
    url = f'https://raw.githubusercontent.com/IBM/plex/{ibm_revision}/packages/plex-sans/fonts/complete/woff2/{name}'
    target = cache / name
    if not target.exists():
        urllib.request.urlretrieve(url,target)
    digest = hashlib.sha256(target.read_bytes()).hexdigest()
    if url in expected and digest != expected[url]:
        raise ValueError(f'Upstream source checksum mismatch: {name}')
    (out/name).write_bytes(target.read_bytes())
    manifest['sources'].append({'url':url,'sha256':digest})
manifest_path.write_text(json.dumps(manifest,indent=2)+'\n')
