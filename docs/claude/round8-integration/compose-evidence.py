from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

root = Path(__file__).resolve().parents[1]
font = ImageFont.truetype('/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc', 22)

def pair(left, right, output):
    a, b = Image.open(left).convert('RGB'), Image.open(right).convert('RGB')
    result = Image.new('RGB', (a.width + b.width, max(a.height, b.height) + 40), '#e7e6e0')
    result.paste(a, (0, 40))
    result.paste(b, (a.width, 40))
    draw = ImageDraw.Draw(result)
    draw.text((22, 6), '设计稿', fill='#29303d', font=font)
    draw.text((a.width + 22, 6), '实现', fill='#29303d', font=font)
    if output.suffix == '.jpg':
        result.save(output, quality=93, subsampling=0)
    else:
        result.save(output)

panels = root / 'round8-panels'
for left in sorted(panels.glob('design-*.png')):
    suffix = left.name.removeprefix('design-')
    pair(left, panels / ('implementation-' + suffix), panels / ('compare-' + suffix))

periphery = root / 'round8-periphery'
for left in sorted(periphery.glob('design-*.png')):
    suffix = left.name.removeprefix('design-')
    right = periphery / ('app-' + suffix)
    if right.exists():
        pair(left, right, periphery / ('compare-' + suffix))

starmap = root / 'round8-starmap'
if starmap.exists():
    for left in sorted(starmap.glob('reference-*.png')):
        suffix = left.name.removeprefix('reference-')
        right = starmap / ('actual-' + suffix)
        if right.exists():
            pair(left, right, starmap / ('compare-' + suffix.removesuffix('.png') + '.jpg'))
