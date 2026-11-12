const fs = require('fs');
const path = require('path');

const UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36';
const url = 'https://fonts.googleapis.com/css2?family=Lalezar&family=Vazirmatn:wght@400;500;600;700;800&family=IBM+Plex+Mono:wght@400;500&display=swap';

const families = {
  'Lalezar': { slug: 'lalezar', subsets: ['arabic', 'latin', 'latin-ext'] },
  'Vazirmatn': { slug: 'vazirmatn', subsets: ['arabic', 'latin', 'latin-ext'] },
  'IBM Plex Mono': { slug: 'plexmono', subsets: ['latin', 'latin-ext'] },
};

(async () => {
  const res = await fetch(url, { headers: { 'User-Agent': UA } });
  const css = await res.text();
  const outDir = path.join('web', 'assets', 'fonts');
  fs.mkdirSync(outDir, { recursive: true });

  const blocks = [...css.matchAll(/\/\*\s*([\w-]+)\s*\*\/\s*@font-face\s*\{([^}]+)\}/g)];
  const rules = [];
  let downloaded = 0;

  for (const [, subset, body] of blocks) {
    const family = /font-family:\s*'([^']+)'/.exec(body)?.[1];
    const weight = /font-weight:\s*(\d+)/.exec(body)?.[1];
    const src = /url\((https:[^)]+)\)/.exec(body)?.[1];
    const range = /unicode-range:\s*([^;]+);/.exec(body)?.[1];
    if (!family || !weight || !src || !range) continue;
    const cfg = families[family];
    if (!cfg || !cfg.subsets.includes(subset)) continue;

    const file = `${cfg.slug}-${weight}-${subset}.woff2`;
    const target = path.join(outDir, file);
    if (!fs.existsSync(target)) {
      const fontRes = await fetch(src, { headers: { 'User-Agent': UA } });
      if (!fontRes.ok) throw new Error(`font download failed ${src}: ${fontRes.status}`);
      fs.writeFileSync(target, Buffer.from(await fontRes.arrayBuffer()));
      downloaded++;
    }
    rules.push(`@font-face {
  font-family: '${family}';
  font-style: normal;
  font-weight: ${weight};
  font-display: swap;
  src: url('/assets/fonts/${file}') format('woff2');
  unicode-range: ${range};
}`);
  }

  fs.writeFileSync(path.join('web', 'assets', 'fonts.css'), rules.join('\n\n') + '\n');
  console.log(`downloaded=${downloaded} rules=${rules.length}`);
  const total = fs.readdirSync(outDir).reduce((sum, f) => sum + fs.statSync(path.join(outDir, f)).size, 0);
  console.log(`font bytes=${total}`);
})().catch((err) => { console.error(err); process.exit(1); });
