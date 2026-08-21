// Build del frontend: bundle + nombre con hash de contenido.
//
// El hash es lo que hace que un deploy se vea SIEMPRE. En F2b el usuario tuvo
// que abrir una ventana de incógnito para ver los cambios: el bundle estaba
// desplegado (grep sobre el servidor lo confirmaba) pero el navegador seguía
// con la copia anterior de "app.js", que es la misma URL de siempre. Con
// app.<hash>.js cada versión es una URL distinta y no hay caché que valga.
import { build } from 'esbuild';
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync, readdirSync, unlinkSync, mkdirSync } from 'node:fs';

mkdirSync('dist', { recursive: true });
// bundles viejos con hash: fuera, para que rsync --delete no deje basura
for (const f of readdirSync('dist')) {
  if (/^app\.[0-9a-f]{8}\.js$/.test(f)) unlinkSync(`dist/${f}`);
}

await build({ entryPoints: ['src/app.js'], bundle: true, minify: true, outfile: 'dist/app.js' });
await build({ entryPoints: ['src/spike.js'], bundle: true, minify: true, outfile: 'dist/spike.js' });

const bundle = readFileSync('dist/app.js');
const hash = createHash('sha256').update(bundle).digest('hex').slice(0, 8);
writeFileSync(`dist/app.${hash}.js`, bundle);

const html = readFileSync('src/index.html', 'utf8').replace('src="app.js"', `src="app.${hash}.js"`);
writeFileSync('dist/index.html', html);
console.log(`dist/app.${hash}.js  (${(bundle.length / 1024).toFixed(1)} kB)`);
