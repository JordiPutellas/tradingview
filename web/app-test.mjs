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

// 7-9. Los dibujos tienen su propia suite con gestos reales: draw-test.mjs
//      (crear, arrastrar en dos ejes, seleccionar, redimensionar, panel,
//      zona de dos niveles, medición y persistencia).

// 10. F2b — colores REALES en el canvas, no las opciones de la serie.
// El test de F2a comprobaba series.options(): eso solo dice qué pedimos, no
// qué se pintó. Aquí se leen los píxeles del canvas del panel principal.
const readPixels = () => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  const counts = new Map();
  let total = 0, black = 0;
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;   // fuera escalas de precio/tiempo
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i + 3] === 0) continue;                  // capa de crosshair, transparente
      const k = `${d[i]},${d[i + 1]},${d[i + 2]}`;
      counts.set(k, (counts.get(k) || 0) + 1);
      total++;
      if (d[i] + d[i + 1] + d[i + 2] === 0) black++;
    }
  }
  const top = [...counts].sort((a, b) => b[1] - a[1]).slice(0, 4);
  const n = (k) => counts.get(k) || 0;
  return { top, total, black,
    up: n('112,146,190'), down: n('218,218,218'),
    viejoUp: n('38,166,154'), viejoDown: n('239,83,80'), fondo: n('54,54,54') };
};
const px = await page.evaluate(`(${readPixels.toString()})()`);
check('fondo #363636 pintado (color dominante del canvas)',
  px.top[0][0] === '54,54,54' && px.fondo > px.total * 0.5,
  `dominante ${px.top[0][0]} (${((px.fondo / px.total) * 100).toFixed(1)}% del canvas)`);
check('velas alcistas #7092be pintadas', px.up > 1000, `${px.up} px`);
check('velas bajistas #dadada pintadas', px.down > 1000, `${px.down} px`);
check('sin verde/rojo por defecto en el canvas', px.viejoUp === 0 && px.viejoDown === 0,
  `#26a69a=${px.viejoUp} #ef5350=${px.viejoDown}`);
check('sin outline negro', px.black === 0, `${px.black} px negros`);
const margins = await page.evaluate(() => window.__test.chart.priceScale('right').options().scaleMargins);
check('margen superior del precio reducido', margins.top <= 0.08, JSON.stringify(margins));

// 10c. F2b — sin rejilla: una franja vacía del panel es fondo puro
const gridProbe = await page.evaluate(() => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  const pane = cs.find(c => c.width * c.height >= area * 0.9);
  const d = pane.getContext('2d').getImageData(0, 8, pane.width, 1).data;
  let distintos = 0;
  for (let i = 0; i < d.length; i += 4) {
    if (d[i + 3] !== 0 && !(d[i] === 54 && d[i + 1] === 54 && d[i + 2] === 54)) distintos++;
  }
  return { distintos, ancho: pane.width };
});
check('sin rejilla (franja superior toda de fondo)', gridProbe.distintos === 0,
  `${gridProbe.distintos}/${gridProbe.ancho} px distintos del fondo`);
check('sin rastro del gris de la rejilla anterior',
  (px.top.find(([k]) => k === '66,66,66' || k === '67,67,67') || [null, 0])[1] === 0);

// 10d. F2b — crosshair sólido, oscuro y de 1 px
await page.mouse.move(700, 400);
await page.waitForTimeout(400);
const cross = await page.evaluate(() => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  let mejor = { run: 0, columnas: 0, total: 0, alto: 0 };
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;
    const { width: w, height: h } = c;
    const d = c.getContext('2d').getImageData(0, 0, w, h).data;
    let columnas = 0, run = 0, total = 0;
    for (let x = 0; x < w; x++) {
      let seguidos = 0;
      for (let y = 0; y < h; y++) {
        const i = (y * w + x) * 4;
        if (d[i] === 30 && d[i + 1] === 30 && d[i + 2] === 30 && d[i + 3] === 255) { seguidos++; total++; }
      }
      if (seguidos > h * 0.9) columnas++;
      run = Math.max(run, seguidos);
    }
    if (total > mejor.total) mejor = { run, columnas, total, alto: h };
  }
  return mejor;
});
check('crosshair pintado en #1e1e1e', cross.total > 100, `${cross.total} px`);
check('crosshair sólido (no discontinuo)', cross.run > cross.alto * 0.9,
  `tramo continuo ${cross.run}/${cross.alto} px`);
check('crosshair de 1 px de ancho', cross.columnas === 1, `${cross.columnas} columnas completas`);
await page.mouse.move(20, 780); // fuera del gráfico: quita el crosshair

// 10b. F2b — el volumen ya no está
const seriesCount = await page.evaluate(() => window.__test.chart.panes()[0].getSeries().length);
check('sin indicador de volumen', seriesCount === 1, `${seriesCount} serie(s) en el panel`);

// 11. F2b — la rueda mueve lo que dice la config, y el ajuste se lee de verdad
const wheelRatio = async () => {
  const before = await page.evaluate(() => window.__test.chart.timeScale().getVisibleLogicalRange());
  await page.mouse.move(700, 400);
  await page.mouse.wheel(0, 100);
  await page.waitForTimeout(250);
  const after = await page.evaluate(() => window.__test.chart.timeScale().getVisibleLogicalRange());
  return (after.to - after.from) / (before.to - before.from);
};
const r1 = await wheelRatio();
const want1 = await page.evaluate(() => 1 + window.__test.CONFIG.wheelZoom);
check('una muesca escala el rango visible según CONFIG', Math.abs(r1 - want1) < 0.02,
  `ratio=${r1.toFixed(3)} esperado=${want1}`);
// cambiar el ajuste debe cambiar el efecto (si LWC siguiera mandando, no lo haría)
await page.evaluate(() => localStorage.setItem('cfg.wheelZoom', '0.6'));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
const r2 = await wheelRatio();
check('cfg.wheelZoom se lee al arrancar y manda sobre LWC', Math.abs(r2 - 1.6) < 0.02,
  `ratio=${r2.toFixed(3)} esperado=1.6`);
await page.evaluate(() => localStorage.removeItem('cfg.wheelZoom'));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });

// 11b. F2b — alejar hasta el tope no puede dejar la pantalla en blanco
await page.mouse.move(700, 400);
for (let i = 0; i < 40; i++) await page.mouse.wheel(0, 100);
await page.waitForTimeout(2500);
const zoomOut = await page.evaluate(`(${readPixels.toString()})()`);
const zr = await page.evaluate(() => {
  const r = window.__test.chart.timeScale().getVisibleLogicalRange();
  const n = window.__test.getBars().length;
  return { from: r.from, to: r.to, n, dentro: Math.min(r.to, n) - Math.max(r.from, 0) };
});
const vacio = (zr.to - zr.n) / (zr.to - zr.from);   // fracción de pantalla sin datos a la derecha
check('el contrazoom máximo deja velas en pantalla',
  zoomOut.up + zoomOut.down > 1000 && zr.dentro > (zr.to - zr.from) * 0.5 && vacio < 0.05,
  `${zoomOut.up + zoomOut.down} px de vela · rango ${zr.from.toFixed(0)}..${zr.to.toFixed(0)} sobre ${zr.n} velas · ${(vacio * 100).toFixed(1)}% vacío a la derecha`);

// 12. F2b — autoescala con el GESTO del usuario: dibujo lejos del precio
// visible (dibujado a 77k, mirando 2019 a 10k) y doble click en la escala.
// El de F2a llamaba a applyOptions({autoScale:true}) y medía coordenadas; este
// hace lo mismo que el usuario y mide el rango de precio que queda a la vista.
async function autoscaleProbe() {
  await page.click('button[data-tf="1D"]');
  await page.waitForFunction(() => window.__test.getTF().name === '1D' && window.__test.getBars().length > 100, { timeout: 20000 });
  const far = await page.evaluate(() => {
    const { getBars, engine, chart } = window.__test;
    const bars = getBars();
    const price = bars.at(-1)[4];
    engine.addShape('hline', [{ t: bars.at(-1)[0], p: price }], { id: 'zz-autoscale-test' });
    // irse al principio del histórico: allí el precio es una fracción del actual
    chart.timeScale().setVisibleLogicalRange({ from: 0, to: 120 });
    return price;
  });
  await page.waitForTimeout(600);
  const box = await page.locator('#chart').boundingBox();
  await page.mouse.dblclick(box.x + box.width - 35, box.y + box.height / 2); // escala de precio → AUTO
  await page.waitForTimeout(600);
  const view = await page.evaluate(() => {
    const { series } = window.__test;
    const h = document.getElementById('chart').clientHeight;
    return { top: series.coordinateToPrice(0), bottom: series.coordinateToPrice(h) };
  });
  await page.evaluate(() => {
    const e = window.__test.engine;
    e.shapes = e.shapes.filter(s => s.id !== 'zz-autoscale-test');
    e.redraw();
    fetch('/api/drawings/zz-autoscale-test', { method: 'DELETE' });
  });
  return { far, ...view };
}
const as = await autoscaleProbe();
check('el autoajuste ignora los dibujos (gesto real)', as.top < as.far * 0.5,
  `dibujo en ${as.far.toFixed(0)}, vista tras AUTO ${as.bottom.toFixed(0)}–${as.top.toFixed(0)}`);

// 12b. control: con drawingsAutoscale=1 el dibujo SÍ debe estirar la escala.
// Sin esto, 12 pasaría igual aunque el arreglo no hiciera nada.
await page.evaluate(() => localStorage.setItem('cfg.drawingsAutoscale', '1'));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
const ctrl = await autoscaleProbe();
check('control: con la opción activada el dibujo sí estira la escala', ctrl.top >= ctrl.far,
  `dibujo en ${ctrl.far.toFixed(0)}, vista tras AUTO ${ctrl.bottom.toFixed(0)}–${ctrl.top.toFixed(0)}`);
await page.evaluate(() => localStorage.removeItem('cfg.drawingsAutoscale'));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });

// 13. F2a — barra de timeframes: una sola línea con scroll lateral
await page.setViewportSize({ width: 720, height: 800 });
await page.waitForTimeout(300);
const tfbar = await page.evaluate(() => {
  const el = window.__test.tfsEl;
  const bs = [...el.querySelectorAll('button')];
  const before = el.scrollLeft;
  el.dispatchEvent(new WheelEvent('wheel', { deltaY: 300, bubbles: true, cancelable: true }));
  return {
    headerH: document.querySelector('header').offsetHeight,
    unaLinea: bs[0].offsetTop === bs[bs.length - 1].offsetTop,
    desborda: el.scrollWidth > el.clientWidth + 4,
    scrollDelta: el.scrollLeft - before,
  };
});
check('timeframes en una sola línea con ventana estrecha',
  tfbar.unaLinea && tfbar.headerH < 45, JSON.stringify(tfbar));
check('la rueda desplaza la barra de timeframes', tfbar.desborda && tfbar.scrollDelta > 0, JSON.stringify(tfbar));
await page.setViewportSize({ width: 1400, height: 800 });

// 14. F2a — barra de dibujo flotante: se arrastra y la posición persiste
const grip = await page.locator('#toolsGrip').boundingBox();
await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2);
await page.mouse.down();
await page.mouse.move(grip.x + 320, grip.y + 240, { steps: 10 });
await page.mouse.up();
await page.waitForTimeout(200);
const moved = await page.evaluate(() => ({
  left: window.__test.toolsEl.style.left, top: window.__test.toolsEl.style.top,
  saved: localStorage.getItem('btcdash.toolbarPos'),
}));
check('barra de dibujo arrastrable', parseFloat(moved.left) > 250 && parseFloat(moved.top) > 200, JSON.stringify(moved));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
const kept = await page.evaluate(() => ({
  left: window.__test.toolsEl.style.left, top: window.__test.toolsEl.style.top,
}));
check('posición de la barra persiste entre sesiones',
  kept.left === moved.left && kept.top === moved.top, `${JSON.stringify(moved)} → ${JSON.stringify(kept)}`);
await page.evaluate(() => localStorage.removeItem('btcdash.toolbarPos'));

// limpieza: borra los dibujos de prueba de la BD
for (const d of await (await fetch('http://127.0.0.1:8090/api/drawings')).json()) {
  await fetch(`http://127.0.0.1:8090/api/drawings/${d.id}`, { method: 'DELETE' });
}

await browser.close();
console.log(fails.length ? `\nFALLOS: ${fails.join(', ')}` : '\nTODO OK');
process.exit(fails.length ? 1 : 0);
