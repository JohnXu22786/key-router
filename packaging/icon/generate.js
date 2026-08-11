const sharp = require('sharp');
const pngToIco = require('png-to-ico').default || require('png-to-ico');
const fs = require('fs');
const path = require('path');

const src = path.join(__dirname, 'app-icon.svg');
const outDir = __dirname;

async function main() {
  // 1. Render SVG to a 256px PNG (base).
  const base = await sharp(src).png().toBuffer();

  // 2. Windows .ico (multi-size: 16/24/32/48/64/128/256).
  const sizes = [16, 24, 32, 48, 64, 128, 256];
  const pngs = [];
  for (const s of sizes) {
    pngs.push(await sharp(base).resize(s, s).png().toBuffer());
  }
  const ico = await pngToIco(pngs);
  fs.writeFileSync(path.join(outDir, 'app-icon.ico'), ico);

  // 3. 512px PNG (Linux desktop / web favicon / high-res).
  await sharp(base).resize(512, 512).png().toFile(path.join(outDir, 'app-icon-512.png'));
  // favicon 32px
  await sharp(base).resize(32, 32).png().toFile(path.join(outDir, 'favicon-32.png'));

  console.log('generated:', fs.readdirSync(outDir).join(', '));
}

main().catch(e => { console.error(e); process.exit(1); });
