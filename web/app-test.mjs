// Test funcional headless del frontend F1c contra la API real.
// Uso: node app-test.mjs  (API en 127.0.0.1:8090 sirviendo web/dist)
import { chromium } from 'playwright';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
page.on('pageerror', e => console.error('PAGE ERROR:', e.message));
const fails = [];
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'OK ' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) fails.push(name);
};

await page.goto('http://127.0.0.1:8090/');
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });

// 1. carga inicial
const n0 = await page.evaluate(() => window.__test.getBars().length);
check('carga inicial 1m', n0 >= 1000, `${n0} velas`);

// 2. DST Europe/Madrid — marzo (01:00Z salta a 03:00 CEST) y octubre (vuelve a 02:00 CET)
const dst = await page.evaluate(() => {
  const f = window.__test.fmtFull;
  return {
    beforeMar: f(Date.UTC(2026, 2, 29, 0, 59, 59) / 1000),
    afterMar: f(Date.UTC(2026, 2, 29, 1, 0, 0) / 1000),
    beforeOct: f(Date.UTC(2026, 9, 25, 0, 59, 59) / 1000),
    afterOct: f(Date.UTC(2026, 9, 25, 1, 0, 0) / 1000),
  };
});
check('DST marzo: 00:59:59Z → 01:59:59 CET', dst.beforeMar.includes('01:59:59'), dst.beforeMar);
check('DST marzo: 01:00:00Z → 03:00:00 CEST', dst.afterMar.includes('03:00:00'), dst.afterMar);
check('DST octubre: 00:59:59Z → 02:59:59 CEST', dst.beforeOct.includes('02:59:59'), dst.beforeOct);
check('DST octubre: 01:00:00Z → 02:00:00 CET', dst.afterOct.includes('02:00:00'), dst.afterOct);

// 3. anclajes cliente = anclajes API
const anchors = await page.evaluate(() => {
  const b = window.__test.bucketStart;
  const tf = (n) => window.__test.TFS.find(t => t.name === n);
  return {
    week: b(tf('1W'), Date.UTC(2026, 7, 21, 15, 0) / 1000),   // viernes → lunes 17
    month: b(tf('1M'), Date.UTC(2026, 7, 21) / 1000),
    quarter: b(tf('3M'), Date.UTC(2026, 7, 21) / 1000),
  };
});
check('1W ancla en lunes', anchors.week === Date.UTC(2026, 7, 17) / 1000, new Date(anchors.week * 1000).toUTCString());
check('1M ancla a día 1', anchors.month === Date.UTC(2026, 7, 1) / 1000);
check('3M ancla a julio', anchors.quarter === Date.UTC(2026, 6, 1) / 1000);
const weekBars = await (await fetch('http://127.0.0.1:8090/api/candles?tf=1W&limit=4')).json();
check('API 1W devuelve lunes 00:00Z', weekBars.every(r => new Date(r[0] * 1000).getUTCDay() === 1 && r[0] % 86400 === 0),
  weekBars.map(r => new Date(r[0] * 1000).toUTCString()).join(' | '));

// 4. cambio de timeframe por UI
await page.click('button[data-tf="1h"]');
await page.waitForFunction(() => window.__test.getTF().name === '1h' && window.__test.getBars().length > 100, { timeout: 20000 });
check('cambio a 1h por UI', true);

// 5. lazy-loading al desplazarse al pasado
const before = await page.evaluate(() => window.__test.getBars().length);
await page.evaluate(() => {
  const r = window.__test; // fuerza el rango visible al principio del histórico cargado
  document.title = 'trigger';
});
await page.evaluate(() => {
  window.__lwcChart = null; // el chart no está expuesto; empuja por teclado/rueda no disponible: usa API pública
});
// dispara el prepend pidiendo rango al inicio: simula con scroll de rueda hacia la izquierda
await page.mouse.move(700, 400);
for (let i = 0; i < 30; i++) await page.mouse.wheel(-600, 0);
await page.waitForTimeout(3000);
const after = await page.evaluate(() => window.__test.getBars().length);
check('lazy-loading histórico', after > before, `${before} → ${after} velas`);

// 6. streaming en vivo (1h en curso se actualiza)
await page.click('button[data-tf="1s"]');
await page.waitForFunction(() => window.__test.getTF().name === '1s' && window.__test.getBars().length > 100, { timeout: 20000 });
const lastT0 = await page.evaluate(() => window.__test.getBars().at(-1)[0]);
await page.waitForTimeout(4000);
const { lastT1, fresh } = await page.evaluate(() => {
  const t = window.__test.getBars().at(-1)[0];
  return { lastT1: t, fresh: Date.now() / 1000 - t };
});
check('streaming 1s en vivo', lastT1 > lastT0 && fresh < 10, `última vela hace ${fresh.toFixed(1)}s`);

// 7. dibujo: crear horizontal por UI, verificar persistencia en API, recargar
await page.click('button[data-tool="hline"]');
await page.mouse.click(650, 300);
await page.waitForTimeout(800);
const drawn = await page.evaluate(() => window.__test.dm.getAllDrawings().map(d => ({ id: d.id, type: d.type })));
check('horizontal creada', drawn.some(d => d.type === 'horizontal-line'), JSON.stringify(drawn));
const apiDrawings = await (await fetch('http://127.0.0.1:8090/api/drawings')).json();
check('persistida en API', apiDrawings.some(d => d.payload?.kind === 'plugin' && d.payload.data.type === 'horizontal-line'));

// 8. zona de dos niveles: dos clicks, mueve junta, persiste
// clicks espaciados: LWC absorbe dos clicks casi simultáneos en el mismo px
await page.click('button[data-tool="zone2"]');
await page.mouse.click(750, 250);
await page.waitForTimeout(600);
await page.mouse.click(850, 350);
await page.waitForTimeout(800);
const zone = await page.evaluate(() => {
  const { zones, dm, series, chart } = window.__test;
  const zs = [...zones.keys()];
  if (!zs.length) return null;
  const id = zs[0];
  const a = dm.getDrawing(id + ':a'), b = dm.getDrawing(id + ':b');
  if (!a || !b) return { missing: true };
  const pa0 = a.anchors[0].price, pb0 = b.anchors[0].price;
  // drag REAL del ancla de A con eventos sintéticos: deben moverse LAS DOS
  dm.selectDrawing(id + ':a');
  const cont = document.getElementById('chart');
  const rect = cont.getBoundingClientRect();
  const x = chart.timeScale().timeToCoordinate(a.anchors[0].time);
  const y = series.priceToCoordinate(pa0);
  const fire = (type, px, py) => cont.dispatchEvent(new MouseEvent(type, {
    bubbles: true, clientX: rect.left + px, clientY: rect.top + py, button: 0 }));
  fire('mousedown', x, y);
  for (let i = 1; i <= 8; i++) fire('mousemove', x, y - 5 * i);
  fire('mouseup', x, y - 40);
  return { pa0, pb0, dA: a.anchors[0].price - pa0, dB: b.anchors[0].price - pb0 };
});
check('zona 2 niveles con precios reales', zone && !zone.missing && zone.pa0 > 1000 && zone.pb0 > 1000, JSON.stringify(zone));
check('zona se mueve JUNTA manteniendo distancia', !!zone && zone.dA !== 0 && Math.abs(zone.dA - zone.dB) < Math.abs(zone.dA) * 0.01,
  zone ? `dA=${zone.dA?.toFixed(2)} dB=${zone.dB?.toFixed(2)}` : 'sin zona');
const apiDrawings2 = await (await fetch('http://127.0.0.1:8090/api/drawings')).json();
check('zona persistida', apiDrawings2.some(d => d.payload?.kind === 'zone2'));

// 9. recarga: los dibujos sobreviven a la sesión
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await page.waitForTimeout(1000);
const restored = await page.evaluate(() => ({
  types: window.__test.dm.getAllDrawings().map(d => d.type).sort(),
  zonePrices: [...window.__test.zones.values()].map(z => [z.a.price, z.b.price]),
}));
check('dibujos restaurados tras recarga',
  restored.types.includes('horizontal-line') && restored.types.filter(t => t === 'horizontal-ray').length === 2,
  JSON.stringify(restored.types));
check('zona restaurada con precios (no timestamps)',
  restored.zonePrices.every(([a, b]) => a > 1000 && a < 1e6 && b > 1000 && b < 1e6),
  JSON.stringify(restored.zonePrices));

// limpieza: borra los dibujos de prueba de la BD
for (const d of await (await fetch('http://127.0.0.1:8090/api/drawings')).json()) {
  await fetch(`http://127.0.0.1:8090/api/drawings/${d.id}`, { method: 'DELETE' });
}

await browser.close();
console.log(fails.length ? `\nFALLOS: ${fails.join(', ')}` : '\nTODO OK');
process.exit(fails.length ? 1 : 0);
