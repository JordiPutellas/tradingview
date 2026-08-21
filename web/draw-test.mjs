// Suite de dibujos (F3) con GESTOS REALES: mousedown / mousemove / mouseup a
// coordenadas concretas. Nada de "existe el método drag": cada comprobación
// mira el efecto — el modelo movido, el rango del gráfico intacto y los
// píxeles pintados en el canvas.
//
// Uso: node draw-test.mjs   (API en 127.0.0.1:8090 sirviendo web/dist)
import { chromium } from 'playwright';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
page.on('pageerror', e => console.error('PAGE ERROR:', e.message));
const fails = [];
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'OK ' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) fails.push(name);
};
const wait = (ms) => page.waitForTimeout(ms);

await page.goto('http://127.0.0.1:8090/');
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(600);

// limpieza previa: la BD es compartida con el usuario
await page.evaluate(async () => {
  for (const d of await (await fetch('/api/drawings')).json()) {
    await fetch(`/api/drawings/${d.id}`, { method: 'DELETE' });
  }
  window.__test.engine.shapes = [];
  window.__test.engine.redraw();
});

const estado = () => page.evaluate(() => {
  const e = window.__test.engine;
  const r = window.__test.chart.timeScale().getVisibleLogicalRange();
  return {
    n: e.shapes.length,
    sel: e.selectedId,
    tipos: e.shapes.map(s => s.type),
    figuras: e.shapes.map(s => ({ id: s.id, type: s.type, style: s.style,
      pts: s.points.map(q => ({ t: q.t, p: q.p })) })),
    rango: { from: Math.round(r.from * 100) / 100, to: Math.round(r.to * 100) / 100 },
    panel: !document.getElementById('drawPanel').hidden,
  };
});

// Cuenta píxeles por color en el canvas del panel (el mismo lector que F2b).
const pixeles = () => page.evaluate(() => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  const counts = new Map();
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i + 3] === 0) continue;
      const k = `${d[i]},${d[i + 1]},${d[i + 2]}`;
      counts.set(k, (counts.get(k) || 0) + 1);
    }
  }
  return Object.fromEntries(counts);
});
const cuenta = (px, rgb) => px[rgb] || 0;

async function arrastrar(x1, y1, x2, y2, { shift = false, pasos = 12 } = {}) {
  await page.mouse.move(x1, y1);
  if (shift) await page.keyboard.down('Shift');
  await page.mouse.down();
  for (let i = 1; i <= pasos; i++) {
    await page.mouse.move(x1 + ((x2 - x1) * i) / pasos, y1 + ((y2 - y1) * i) / pasos);
  }
  await page.mouse.up();
  if (shift) await page.keyboard.up('Shift');
  await wait(250);
}

const crear = async (tool, puntos) => {
  await page.click(`button[data-tool="${tool}"]`);
  for (const [x, y] of puntos) { await page.mouse.click(x, y); await wait(220); }
  await wait(350);
};

// ---------------------------------------------------------------- 1. crear
const HERRAMIENTAS = [
  ['hline', [[500, 250]]],
  ['hray', [[520, 280]]],
  ['trend', [[400, 500], [700, 400]]],
  ['rect', [[750, 300], [900, 420]]],
  ['curve', [[300, 600], [400, 560], [500, 640], [600, 600]]],
  ['arc', [[950, 500], [1000, 460], [1050, 520]]],
  ['point', [[860, 550]]],
  ['text', [[300, 200]]],
  ['zone2', [[600, 620], [640, 660]]],
];
for (const [tool, pts] of HERRAMIENTAS) await crear(tool, pts);
const creado = await estado();
check('las 9 figuras se crean con clicks reales',
  creado.n === 9 && HERRAMIENTAS.every(([t]) => creado.tipos.includes(t)),
  `${creado.n} figuras: ${creado.tipos.join(', ')}`);
check('cada figura guarda tiempo UTC y precio, no índices',
  creado.figuras.every(f => f.pts.every(q => q.t > 1_500_000_000 && q.p > 1000 && q.p < 1e6)),
  JSON.stringify(creado.figuras[2].pts));

// --------------------------------------------------- 2. arrastre en 2 ejes
// El rectángulo: se agarra por dentro y se mueve en diagonal. Debe moverse el
// DIBUJO y no el gráfico (que era el fallo de raíz del plugin).
await page.mouse.click(825, 360);         // seleccionar el rectángulo
await wait(300);
const antes = await estado();
const rectAntes = antes.figuras.find(f => f.type === 'rect');
await arrastrar(825, 360, 985, 300);      // +160 px en X, -60 px en Y
const despues = await estado();
const rectDespues = despues.figuras.find(f => f.type === 'rect');
const dT = rectDespues.pts[0].t - rectAntes.pts[0].t;
const dP = rectDespues.pts[0].p - rectAntes.pts[0].p;
check('arrastrar mueve el dibujo en el eje X (tiempo)', dT > 0, `${dT}s`);
check('arrastrar mueve el dibujo en el eje Y (precio)', dP > 0, `${dP.toFixed(2)} USD`);
check('arrastrar NO mueve el gráfico',
  antes.rango.from === despues.rango.from && antes.rango.to === despues.rango.to,
  `${JSON.stringify(antes.rango)} → ${JSON.stringify(despues.rango)}`);
// y el segundo punto se movió lo mismo: la figura se traslada entera
const d2T = rectDespues.pts[1].t - rectAntes.pts[1].t;
check('la figura se traslada entera', Math.abs(d2T - dT) <= 1, `p0 ${dT}s vs p1 ${d2T}s`);

// ------------------------------------------------ 3. selección y handles
const conSeleccion = await pixeles();
check('el dibujo seleccionado muestra puntos de control',
  cuenta(conSeleccion, '255,255,255') > 40, `${cuenta(conSeleccion, '255,255,255')} px blancos`);
check('el panel de configuración aparece al seleccionar', despues.panel);
await page.mouse.click(180, 700);         // click en zona vacía
await wait(300);
const deseleccionado = await estado();
const sinSeleccion = await pixeles();
check('click fuera deselecciona', deseleccionado.sel === null && !deseleccionado.panel);
check('sin selección no hay puntos de control',
  cuenta(sinSeleccion, '255,255,255') < cuenta(conSeleccion, '255,255,255') / 2,
  `${cuenta(conSeleccion, '255,255,255')} → ${cuenta(sinSeleccion, '255,255,255')} px`);

// --------------------------------------------- 4. redimensionar por handle
await page.mouse.click(985, 300);          // reseleccionar el rectángulo (esquina)
await wait(300);
const rEstado = await estado();
const r0 = rEstado.figuras.find(f => f.type === 'rect');
const handle = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const pts = e.screenPoints(s);
  const r = e.paneRect();
  return { x: r.left + pts[1].x, y: r.top + pts[1].y };
});
await arrastrar(handle.x, handle.y, handle.x + 90, handle.y + 40);
const r1 = (await estado()).figuras.find(f => f.type === 'rect');
check('el handle redimensiona SOLO su punto',
  Math.abs(r1.pts[0].t - r0.pts[0].t) < 2 && Math.abs(r1.pts[0].p - r0.pts[0].p) < 1
  && r1.pts[1].t > r0.pts[1].t && r1.pts[1].p < r0.pts[1].p,
  `p0 igual (${(r1.pts[0].p - r0.pts[0].p).toFixed(2)}), p1 +${r1.pts[1].t - r0.pts[1].t}s`);

// --------------------------------------------------------- 5. panel en vivo
await page.evaluate(() => {
  const i = document.querySelector('#drawPanel [data-k="color"]');
  i.value = '#ff00ff'; i.dispatchEvent(new Event('input', { bubbles: true }));
});
await wait(400);
const conMagenta = await pixeles();
check('cambiar el color en el panel repinta el dibujo', cuenta(conMagenta, '255,0,255') > 100,
  `${cuenta(conMagenta, '255,0,255')} px magenta`);
const antesGrosor = cuenta(conMagenta, '255,0,255');
await page.evaluate(() => {
  const i = document.querySelector('#drawPanel [data-k="width"]');
  i.value = '4'; i.dispatchEvent(new Event('input', { bubbles: true }));
});
await wait(400);
const conGrosor = cuenta(await pixeles(), '255,0,255');
check('cambiar el grosor engorda el trazo', conGrosor > antesGrosor * 1.4,
  `${antesGrosor} → ${conGrosor} px`);
await page.evaluate(() => {
  const i = document.querySelector('#drawPanel [data-k="dash"]');
  i.value = '1'; i.dispatchEvent(new Event('input', { bubbles: true }));
});
await wait(400);
const conDiscontinua = cuenta(await pixeles(), '255,0,255');
check('la línea discontinua pinta menos píxeles que la sólida',
  conDiscontinua < conGrosor * 0.85, `${conGrosor} → ${conDiscontinua} px`);
await page.evaluate(() => {
  const i = document.querySelector('#drawPanel [data-k="fillOpacity"]');
  i.value = '0.9'; i.dispatchEvent(new Event('input', { bubbles: true }));
  const c = document.querySelector('#drawPanel [data-k="fill"]');
  c.value = '#00ff00'; c.dispatchEvent(new Event('input', { bubbles: true }));
});
await wait(400);
const conRelleno = await pixeles();
const verdes = Object.entries(conRelleno).filter(([k, v]) => {
  const [r, g, b] = k.split(',').map(Number);
  return g > 200 && r < 90 && b < 90 && v > 50;
}).reduce((a, [, v]) => a + v, 0);
check('el relleno del rectángulo se aplica en vivo', verdes > 2000, `${verdes} px de relleno`);

// ------------------------------------------------------- 6. Supr y persistencia
const antesBorrar = (await estado()).n;
await page.keyboard.press('Delete');
await wait(400);
const trasBorrar = await estado();
check('Supr borra el dibujo seleccionado', trasBorrar.n === antesBorrar - 1 && trasBorrar.sel === null,
  `${antesBorrar} → ${trasBorrar.n}`);
const enApi = await page.evaluate(() => fetch('/api/drawings').then(r => r.json()));
check('la API guarda una fila por figura viva', enApi.length === trasBorrar.n,
  `${enApi.length} filas / ${trasBorrar.n} figuras`);
check('el payload guarda tiempo y precio absolutos',
  enApi.every(d => d.payload.kind === 'shape' && d.payload.points.every(p => p.t > 1_500_000_000)),
  JSON.stringify(enApi[0]?.payload?.points?.[0]));

await page.reload();
await page.waitForFunction(() => window.__test && window.__test.engine.shapes.length > 0, { timeout: 30000 });
await wait(600);
const trasRecarga = await estado();
check('los dibujos sobreviven a la recarga',
  trasRecarga.n === trasBorrar.n && trasRecarga.tipos.sort().join() === trasBorrar.tipos.sort().join(),
  `${trasRecarga.n} figuras: ${trasRecarga.tipos.join(', ')}`);

// ------------------------------------------------------------- 7. zona de 2
const zAntes = (await estado()).figuras.find(f => f.type === 'zone2');
const zPos = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'zone2');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { l1: { x: r.left + pts[0].x + 200, y: r.top + pts[0].y },
           h0: { x: r.left + pts[0].x, y: r.top + pts[0].y } };
});
await page.mouse.click(zPos.l1.x, zPos.l1.y);   // seleccionar por la línea A
await wait(300);
await arrastrar(zPos.l1.x, zPos.l1.y, zPos.l1.x, zPos.l1.y - 50);
const zDespues = (await estado()).figuras.find(f => f.type === 'zone2');
const dA = zDespues.pts[0].p - zAntes.pts[0].p, dB = zDespues.pts[1].p - zAntes.pts[1].p;
check('la zona se mueve JUNTA manteniendo la distancia',
  dA > 0 && Math.abs(dA - dB) < Math.abs(dA) * 0.02, `dA=${dA.toFixed(2)} dB=${dB.toFixed(2)}`);
const zPos2 = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'zone2');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + pts[0].x, y: r.top + pts[0].y };
});
const z1 = (await estado()).figuras.find(f => f.type === 'zone2');
await arrastrar(zPos2.x, zPos2.y, zPos2.x, zPos2.y - 40, { shift: true });
const z2 = (await estado()).figuras.find(f => f.type === 'zone2');
check('con Shift el handle ajusta SOLO su línea',
  Math.abs(z2.pts[0].p - z1.pts[0].p) > 1 && Math.abs(z2.pts[1].p - z1.pts[1].p) < 0.5,
  `A ${(z2.pts[0].p - z1.pts[0].p).toFixed(2)} · B ${(z2.pts[1].p - z1.pts[1].p).toFixed(2)}`);

// ------------------------------------------- 7b. cambio de timeframe
// Los dibujos se guardan en tiempo absoluto: al cambiar de TF deben seguir en
// el mismo instante y precio, no en el mismo índice de barra.
const antesTF = (await estado()).figuras.find(f => f.type === 'trend');
await page.click('button[data-tf="1h"]');
await page.waitForFunction(() => window.__test.getTF().name === '1h' && window.__test.getBars().length > 100, { timeout: 20000 });
await wait(800);
const enOtroTF = (await estado()).figuras.find(f => f.type === 'trend');
check('los dibujos sobreviven al cambio de timeframe',
  enOtroTF && enOtroTF.pts[0].t === antesTF.pts[0].t && Math.abs(enOtroTF.pts[0].p - antesTF.pts[0].p) < 1e-6,
  `t=${antesTF.pts[0].t} p=${antesTF.pts[0].p.toFixed(2)}`);
const xEnOtroTF = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'trend');
  return e.screenPoints(s)?.[0]?.x ?? null;
});
check('y se pintan en una coordenada válida del nuevo timeframe',
  xEnOtroTF !== null && Number.isFinite(xEnOtroTF), `x=${xEnOtroTF && xEnOtroTF.toFixed(0)}`);
await page.click('button[data-tf="1m"]');
await page.waitForFunction(() => window.__test.getTF().name === '1m' && window.__test.getBars().length > 100, { timeout: 20000 });
await wait(600);

// -------------------------------------------------------------- 8. medición
await page.keyboard.press('Escape');
await wait(200);
const rangoAntesMedir = (await estado()).rango;
await page.keyboard.down('Shift');
await page.mouse.click(500, 500);
await page.keyboard.up('Shift');
await wait(200);
await page.mouse.move(800, 350, { steps: 10 });   // sin shift, siguiendo al cursor
await wait(400);
const medVivo = await page.evaluate(() => window.__test.engine.measureInfo());
check('shift+click arranca la medición y sigue al cursor',
  medVivo && medVivo.fixed === false && medVivo.bars > 0 && medVivo.delta !== 0,
  medVivo && `Δ${medVivo.delta.toFixed(2)} (${medVivo.pct.toFixed(2)}%) ${medVivo.bars} barras ${medVivo.duration}`);
check('la medición no desplaza el gráfico',
  JSON.stringify((await estado()).rango) === JSON.stringify(rangoAntesMedir));
const px3 = await pixeles();
const verdeMedida = Object.entries(px3).filter(([k]) => {
  const [r, g, b] = k.split(',').map(Number);
  return Math.abs(r - 38) < 40 && Math.abs(g - 166) < 40 && Math.abs(b - 154) < 40;
}).reduce((a, [, v]) => a + v, 0);
check('la medición se pinta (rectángulo + flechas)', verdeMedida > 500, `${verdeMedida} px`);
await page.mouse.click(800, 350);
await wait(300);
const medFija = await page.evaluate(() => window.__test.engine.measureInfo());
check('un click fija la medición', medFija && medFija.fixed === true);
await page.mouse.move(900, 300);
await wait(300);
const medQuieta = await page.evaluate(() => window.__test.engine.measureInfo());
check('fijada, deja de seguir al cursor', Math.abs(medQuieta.delta - medFija.delta) < 1e-9,
  `${medFija.delta.toFixed(2)} vs ${medQuieta.delta.toFixed(2)}`);
await page.mouse.click(900, 300);
await wait(300);
check('otro click la elimina', (await page.evaluate(() => window.__test.engine.measureInfo())) === null);

// --------------------------------------------------- 9. limpieza final
await page.evaluate(async () => {
  for (const d of await (await fetch('/api/drawings')).json()) {
    await fetch(`/api/drawings/${d.id}`, { method: 'DELETE' });
  }
});
await browser.close();
console.log(fails.length ? `\nFALLOS: ${fails.join(', ')}` : '\nTODO OK');
process.exit(fails.length ? 1 : 0);
