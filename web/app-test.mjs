// Test funcional headless del frontend F1c contra la API real.
// Uso: ./run-tests.sh desde la raíz (levanta la BD de test, la siembra y la API)
import { chromium } from 'playwright';
import { existsSync } from 'node:fs';

// Chromium de Playwright necesita libnss3/libnspr4 y en esta máquina no están
// en el sistema (no hay sudo para instalarlas). Si están extraídas en la caché
// del usuario, se le añaden al entorno para que el navegador arranque:
//   mkdir -p ~/.cache/playwright-sys-libs && cd $_ \
//     && apt-get download libnss3 libnspr4 && for d in *.deb; do dpkg-deb -x $d .; done
const LIBS = `${process.env.HOME}/.cache/playwright-sys-libs/usr/lib/x86_64-linux-gnu`;
if (existsSync(LIBS)) {
  process.env.LD_LIBRARY_PATH = [LIBS, process.env.LD_LIBRARY_PATH].filter(Boolean).join(':');
}


const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
page.on('pageerror', e => console.error('PAGE ERROR:', e.message));
const fails = [];
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'OK ' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) fails.push(name);
};


// ---------------------------------------------------------------------------
// Esta suite BORRA dibujos y trabaja a lo bruto: solo puede correr contra la
// base de datos de TEST. La API publica su nombre en /api/health y aquí se
// comprueba antes de tocar nada. En F4 esto no existía, las suites corrían
// contra producción y se llevaron por delante los dibujos del usuario.
//
// Se levanta todo con ./run-tests.sh desde la raíz del repo.
const API = process.env.API_BASE || 'http://127.0.0.1:8090';
const salud = await fetch(`${API}/api/health`).then(r => r.json()).catch(() => ({}));
if (!/_test$/.test(salud.db || '')) {
  console.error(`ABORTADO: la API sirve la base de datos "${salud.db || '?'}", que no es de test.`);
  console.error('Arranca el entorno con ./run-tests.sh (raíz del repo).');
  process.exit(2);
}

// Ventana de la semilla: los tests de navegación se mueven DENTRO de ella, no
// sobre años fijos. La semilla es sintética y su ventana va con el reloj.
const DIA = 86400;
const dias1D = await (await fetch(`${API}/api/candles?tf=1D&from=0&limit=1`)).json();
if (!dias1D.length) {
  console.error('ABORTADO: la base de datos de test está vacía.');
  console.error('Siembra con: docker compose --profile test run --rm seed-test');
  process.exit(2);
}
const primerDia = dias1D[0][0];
const dias = (n) => primerDia + n * DIA;

await page.goto(API + '/');
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
// Restos de una pasada anterior de la propia suite: fuera, que se pintan
// encima de las comprobaciones de píxeles.
await page.evaluate(async () => {
  window.__test.engine.clear();
  for (const d of await (await fetch('/api/drawings')).json()) {
    await fetch(`/api/drawings/${d.id}`, { method: 'DELETE' });
  }
});

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
const weekBars = await (await fetch(`${API}/api/candles?tf=1W&limit=4`)).json();
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

// 6. streaming en vivo (la vela en curso se actualiza)
// Con F4-1.1 el cambio de timeframe conserva el tramo, así que primero se
// pone la vista en un pasado con datos y luego se comprueba que End vuelve.
await page.click('button[data-tf="1s"]');
await page.waitForFunction(() => window.__test.getTF().name === '1s' && window.__test.getBars().length > 100, { timeout: 20000 });
// Ventana explícita en el pasado y DENTRO de la cobertura de 1s. Antes se
// aprovechaba donde hubiera dejado la vista el paneo del test anterior, que no
// es determinista: si cae fuera del tramo con velas de 1s, el frontend hace lo
// correcto —volver al presente— y el check fallaba por el motivo equivocado.
await page.evaluate(() => {
  const ahora = Math.floor(Date.now() / 1000);
  return window.__test.loadTF(window.__test.getTF(), { view: { from: ahora - 5400, to: ahora - 3600 } });
});
await page.waitForTimeout(1800);
check('con la ventana en el pasado el frontend lo sabe',
  (await page.evaluate(() => window.__test.isLive())) === false);
await page.keyboard.press('End');
// isLive() se pone a true al EMPEZAR la carga, así que no vale como señal por
// sí solo: hay que esperar a que las velas estén y lleguen hasta hoy.
await page.waitForFunction(() => {
  const b = window.__test.getBars();
  return window.__test.isLive() && b.length > 100 && Date.now() / 1000 - b[b.length - 1][0] < 120;
}, { timeout: 30000 });
await page.waitForTimeout(800);
check('End vuelve al presente',
  (await page.evaluate(() => Date.now() / 1000 - window.__test.getBars().at(-1)[0])) < 10,
  `${(await page.evaluate(() => Date.now() / 1000 - window.__test.getBars().at(-1)[0])).toFixed(1)}s de antigüedad`);
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

// 10e. F4 — el crosshair se puede quitar (tecla X o botón de la barra)
const cruzPx = () => page.evaluate(() => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  let total = 0;
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i] === 30 && d[i + 1] === 30 && d[i + 2] === 30 && d[i + 3] === 255) total++;
    }
  }
  return total;
});
const legendC = () => page.evaluate(() => {
  const m = document.getElementById('legend').textContent.match(/C ([\d.,]+)/);
  return m ? parseFloat(m[1].replace(/\./g, '').replace(',', '.')) : null;
});
await page.mouse.move(700, 400);
await page.waitForTimeout(400);
const cruzAntes = await cruzPx();
const legConCruz = await legendC();
await page.keyboard.press('x');
await page.waitForTimeout(300);
await page.mouse.move(701, 401);
await page.waitForTimeout(400);
const cruzQuitada = await cruzPx();
check('la tecla X quita el crosshair del lienzo', cruzAntes > 100 && cruzQuitada === 0,
  `${cruzAntes} → ${cruzQuitada} px`);
check('y el botón de la barra queda encendido',
  await page.evaluate(() => document.querySelector('button[data-tool="__cross"]').classList.contains('active')));
check('sin crosshair, la legend sigue siguiendo al cursor',
  (await legendC()) !== null && Math.abs((await legendC()) - legConCruz) < 500,
  `cierre ${legConCruz} → ${await legendC()}`);
// y tampoco quedan etiquetas del crosshair colgando en los ejes
check('las etiquetas del crosshair en los ejes también se van',
  await page.evaluate(() => {
    const o = window.__test.chart.options().crosshair;
    return o.vertLine.labelVisible === false && o.horzLine.labelVisible === false;
  }));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await page.mouse.move(700, 400);
await page.waitForTimeout(600);
check('el interruptor del crosshair se recuerda entre sesiones',
  (await cruzPx()) === 0 && (await page.evaluate(() => window.__test.sinCrosshair())) === true);
await page.click('button[data-tool="__cross"]');
await page.mouse.move(702, 402);
await page.waitForTimeout(500);
const cruzVuelta = await cruzPx();
check('el botón lo vuelve a poner', cruzVuelta > 100, `${cruzVuelta} px`);
await page.evaluate(() => localStorage.removeItem('btcdash.sinCrosshair'));


// 10b. F2b — el volumen ya no está
const seriesCount = await page.evaluate(() => window.__test.chart.panes()[0].getSeries().length);
check('sin indicador de volumen', seriesCount === 1, `${seriesCount} serie(s) en el panel`);

// 11. F2b — la rueda mueve lo que dice la config, y el ajuste se lee de verdad
const wheelRatio = async () => {
  // Ventana moderada de partida: si se mide alejando desde el tope de zoom
  // (que es donde deja el gráfico el test anterior), el recorte de "no
  // alejarse más allá de lo cargado" se come la muesca y el ratio no dice
  // nada de la sensibilidad.
  await page.evaluate(() => {
    const n = window.__test.getBars().length;
    window.__test.chart.timeScale().setVisibleLogicalRange({ from: n - 300, to: n });
  });
  await page.waitForTimeout(400);
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
// La vista guardada (F4-1.3) se limpia: esta comprobación mide la rueda desde
// el estado por defecto (1m, presente), no desde donde quedó el test anterior.
await page.evaluate(() => { localStorage.setItem('cfg.wheelZoom', '0.6'); localStorage.removeItem('btcdash.view'); });
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
const r2 = await wheelRatio();
check('cfg.wheelZoom se lee al arrancar y manda sobre LWC', Math.abs(r2 - 1.6) < 0.02,
  `ratio=${r2.toFixed(3)} esperado=1.6`);
await page.evaluate(() => { localStorage.removeItem('cfg.wheelZoom'); localStorage.removeItem('btcdash.view'); });
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

// 11c. F5a — con hueco a la derecha, la rueda NO pega el precio al borde.
// Se mide en píxeles: la x de la última vela pintada. Antes del arreglo, la
// primera muesca recortaba el rango a n+gap y la vela saltaba al borde derecho
// (frac 0,5 → 1,0), así que el zoom acababa centrado en otro sitio.
const bordeVelas = () => {
  const esVela = (d, i) => (d[i] === 112 && d[i + 1] === 146 && d[i + 2] === 190)
    || (d[i] === 218 && d[i + 1] === 218 && d[i + 2] === 218);
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  let maxX = -1, w = 0;
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;   // fuera escalas de precio/tiempo
    w = c.width;
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    // La línea de último precio cruza TODO el ancho pintada del color de la
    // vela: sin descartarla, la "última vela" salía siempre en el borde
    // derecho y la comprobación no medía nada.
    const anchas = new Set();
    for (let y = 0; y < c.height; y++) {
      let cuenta = 0;
      for (let x = 0; x < w; x++) if (esVela(d, (y * w + x) * 4)) cuenta++;
      if (cuenta > w * 0.5) anchas.add(y);
    }
    for (let y = 0; y < c.height; y++) {
      if (anchas.has(y)) continue;
      for (let x = w - 1; x > maxX; x--) if (esVela(d, (y * w + x) * 4)) { maxX = x; break; }
    }
  }
  return { frac: w ? maxX / w : -1, maxX, w };
};
const conHueco = async () => {
  await page.evaluate(() => {
    const n = window.__test.getBars().length;
    window.__test.chart.timeScale().setVisibleLogicalRange({ from: n - 400, to: n + 400 });
  });
  await page.waitForTimeout(500);
};
const rangoLog = () => page.evaluate(() => {
  const r = window.__test.chart.timeScale().getVisibleLogicalRange();
  return { ...r, n: window.__test.getBars().length, hueco: (r.to - window.__test.getBars().length) / (r.to - r.from) };
});
await conHueco();
const hb0 = await page.evaluate(`(${bordeVelas.toString()})()`);
await page.mouse.move(700, 400);
await page.mouse.wheel(0, 100);                       // alejar una muesca
await page.waitForTimeout(400);
const hb1 = await page.evaluate(`(${bordeVelas.toString()})()`);
const hr1 = await rangoLog();
check('alejando con hueco a la derecha, la última vela no salta al borde',
  Math.abs(hb1.frac - hb0.frac) < 0.06 && hr1.hueco > 0.35,
  `última vela en ${(hb0.frac * 100).toFixed(1)}% → ${(hb1.frac * 100).toFixed(1)}% del ancho · ` +
  `hueco ${(hr1.hueco * 100).toFixed(1)}%`);

await conHueco();
const hb2 = await page.evaluate(`(${bordeVelas.toString()})()`);
await page.mouse.move(400, 400);
for (let i = 0; i < 3; i++) await page.mouse.wheel(0, -100);   // acercar sobre las velas
await page.waitForTimeout(400);
const hb3 = await page.evaluate(`(${bordeVelas.toString()})()`);
const hr3 = await rangoLog();
check('acercando tampoco: el hueco sigue ahí y el zoom es donde apunta el ratón',
  hb3.frac < 0.98 && hr3.hueco > 0.2 && (hr3.to - hr3.from) < 400 * 2 * 0.9,
  `última vela en ${(hb3.frac * 100).toFixed(1)}% · hueco ${(hr3.hueco * 100).toFixed(1)}% · ` +
  `${(hr3.to - hr3.from).toFixed(0)} velas de ventana (eran 800)`);

// y el cambio de temporalidad con las flechas conserva ese mismo hueco
await conHueco();
const hb4 = await page.evaluate(`(${bordeVelas.toString()})()`);
const tf4 = await page.evaluate(() => window.__test.getTF().name);
await page.keyboard.press('ArrowUp');
await page.waitForFunction((t) => window.__test.getTF().name !== t, tf4, { timeout: 20000 });
await page.waitForTimeout(2000);
const hb5 = await page.evaluate(`(${bordeVelas.toString()})()`);
const hr5 = await rangoLog();
check('cambiar de temporalidad con las flechas respeta el hueco',
  Math.abs(hb5.frac - hb4.frac) < 0.08 && hr5.hueco > 0.35,
  `${tf4} → ${await page.evaluate(() => window.__test.getTF().name)} · ` +
  `última vela ${(hb4.frac * 100).toFixed(1)}% → ${(hb5.frac * 100).toFixed(1)}% · ` +
  `hueco ${(hr5.hueco * 100).toFixed(1)}%`);
await page.evaluate(() => { localStorage.removeItem('btcdash.view'); });
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });

// 12. F2b — autoescala con el GESTO del usuario: dibujo lejos del precio
// visible (dibujado a 77k, mirando 2019 a 10k) y doble click en la escala.
// El de F2a llamaba a applyOptions({autoScale:true}) y medía coordenadas; este
// hace lo mismo que el usuario y mide el rango de precio que queda a la vista.
async function autoscaleProbe() {
  await page.click('button[data-tf="1D"]');
  await page.waitForFunction(() => window.__test.getTF().name === '1D' && window.__test.getBars().length > 100, { timeout: 20000 });
  // Una horizontal al TRIPLE del precio visible: si el autoajuste mirara los
  // dibujos, la escala tendría que estirarse hasta ahí. No depende de cómo sea
  // el histórico, solo de que el dibujo esté lejos.
  const far = await page.evaluate(async () => {
    const { getBars, engine } = window.__test;
    const bars = getBars();
    const price = bars.at(-1)[4] * 3;
    engine.addShape('hline', [{ t: bars.at(-1)[0], p: price }], { id: 'zz-autoscale-test' });
    return price;
  });
  await page.waitForTimeout(1500);
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
await page.evaluate(() => { localStorage.setItem('cfg.drawingsAutoscale', '1'); localStorage.removeItem('btcdash.view'); });
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
const ctrl = await autoscaleProbe();
check('control: con la opción activada el dibujo sí estira la escala', ctrl.top >= ctrl.far,
  `dibujo en ${ctrl.far.toFixed(0)}, vista tras AUTO ${ctrl.bottom.toFixed(0)}–${ctrl.top.toFixed(0)}`);
await page.evaluate(() => { localStorage.removeItem('cfg.drawingsAutoscale'); localStorage.removeItem('btcdash.view'); });
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

// 14. F4b — la barra de dibujo es una columna fija en el lateral izquierdo
// (antes flotaba y se arrastraba: con catorce botones tapaba el gráfico).
const barra = await page.evaluate(() => {
  const el = window.__test.toolsEl;
  const bs = [...el.querySelectorAll('button')];
  const r = el.getBoundingClientRect();
  const canvas = [...document.querySelectorAll('#chart canvas')]
    .reduce((a, b) => (a.width * a.height >= b.width * b.height ? a : b));
  return {
    left: r.left, ancho: r.width, alto: r.height,
    columna: bs.every(b => Math.abs(b.getBoundingClientRect().left - bs[0].getBoundingClientRect().left) < 2),
    apilados: bs[bs.length - 1].getBoundingClientRect().top > bs[0].getBoundingClientRect().top + 100,
    graficoEmpiezaDespues: canvas.getBoundingClientRect().left >= r.right - 1,
    botones: bs.length,
    hayPunto: !!el.querySelector('button[data-tool="point"]'),
  };
});
check('la barra de dibujo está pegada al lateral izquierdo',
  barra.left === 0 && barra.ancho < 60, JSON.stringify(barra));
check('y es una columna: los botones van uno debajo de otro',
  barra.columna && barra.apilados, `${barra.botones} botones`);
check('el gráfico empieza donde acaba la barra (no la tapa)',
  barra.graficoEmpiezaDespues, JSON.stringify(barra));
check('la herramienta "punto" ya no está', !barra.hayPunto);

// ============================================================ F4 · navegación
// Regla de F2a/F3: efecto, no intención. Aquí el rango visible se lee con
// getVisibleRange() de LWC — su propio mapeo índice→tiempo, camino distinto
// del que usa el frontend para restaurarlo. Si la conversión estuviera rota,
// las dos no cuadrarían.
const iso = (t) => new Date(t * 1000).toISOString().slice(0, 16);
const vista = () => page.evaluate(() => {
  const ts = window.__test.chart.timeScale();
  const r = ts.getVisibleRange(), l = ts.getVisibleLogicalRange();
  return r && l ? { from: r.from, to: r.to, velas: l.to - l.from } : null;
});
const irTF = async (name) => {
  await page.click(`button[data-tf="${name}"]`);
  await page.waitForFunction((n) => window.__test.getTF().name === n && window.__test.getBars().length > 0,
    name, { timeout: 30000 });
  await page.waitForTimeout(1300);
};
const segs = (name) => TFSEG[name];
const TFSEG = { '1s':1,'5s':5,'10s':10,'15s':15,'30s':30,'45s':45,'1m':60,'3m':180,'5m':300,
  '15m':900,'30m':1800,'45m':2700,'1h':3600,'2h':7200,'3h':10800,'4h':14400,'6h':21600,
  '8h':28800,'12h':43200,'1D':86400,'3D':259200,'5D':432000,'1W':604800,'2W':1209600,
  '1M':2592000,'3M':7776000,'6M':15552000,'12M':31536000 };
const mismoRango = (a, b, tfA, tfB) => {
  const tol = segs(tfA) + segs(tfB);
  return Math.abs(a.from - b.from) <= tol && Math.abs(a.to - b.to) <= tol;
};

// 15.1 — cerca del presente: 1h → 5m conserva el tramo (RF-5.17)
await irTF('1h');
await page.evaluate(() => {
  const b = window.__test.getBars();
  const ts = window.__test.chart.timeScale();
  ts.setVisibleRange({ from: b[b.length - 120][0], to: b[b.length - 1][0] });  // API de LWC
});
await page.waitForTimeout(900);
const v1h = await vista();
await irTF('5m');
const v5m = await vista();
check('el rango visible sobrevive al cambio de timeframe (1h→5m)',
  mismoRango(v1h, v5m, '1h', '5m'), `${iso(v1h.from)}…${iso(v1h.to)} → ${iso(v5m.from)}…${iso(v5m.to)}`);
check('y el destino trae las velas que tocan', v5m.velas > 1000 && v5m.velas < 1600,
  `${Math.round(v5m.velas)} velas de 5m para ${Math.round(v1h.velas)} de 1h`);

// 15.2 — el principio del histórico cargado, no solo cerca del presente
await irTF('1D');
await page.evaluate((v) => window.__test.loadTF(window.__test.getTF(), { view: v }),
  { from: dias(20), to: dias(50) });
await page.waitForTimeout(2500);
const v2020 = await vista();
check('se puede saltar a un tramo del principio del histórico',
  Math.abs(v2020.from - dias(20)) < 2 * DIA, iso(v2020.from));
await irTF('1h');
const v2020h = await vista();
check('el rango se conserva también en el pasado (1D→1h)',
  mismoRango(v2020, v2020h, '1D', '1h'), `${iso(v2020.from)}…${iso(v2020.to)} → ${iso(v2020h.from)}…${iso(v2020h.to)}`);
check('y con la ventana en el pasado el streaming no toca las velas',
  (await page.evaluate(() => window.__test.isLive())) === false);

// 15.3 — tope de velas: 1D sobre un año → 1m centra en el mismo instante
await irTF('1D');
await page.evaluate((v) => window.__test.loadTF(window.__test.getTF(), { view: v }),
  { from: dias(5), to: dias(370) });
await page.waitForTimeout(2500);
const vAnio = await vista();
await irTF('1m');
const vMin = await vista();
const cap = await page.evaluate(() => window.__test.CONFIG.tfChangeMaxBars);
const centro = (r) => (r.from + r.to) / 2;
check('sobre el tope, el cambio de timeframe centra en el mismo instante',
  Math.abs(centro(vMin) - centro(vAnio)) < 2 * 86400,
  `centro ${iso(centro(vAnio))} → ${iso(centro(vMin))}`);
check('y muestra como mucho el tope de velas', vMin.velas <= cap * 1.02 && vMin.velas > cap * 0.9,
  `${Math.round(vMin.velas)} velas (tope ${cap})`);
check('el aviso del tope se ve en la barra de estado',
  (await page.evaluate(() => document.getElementById('status').textContent)).includes('tope'),
  await page.evaluate(() => document.getElementById('status').textContent));

// 15.4 — suelo de velas: de 1s a 1h no puede quedar una vela sola
await irTF('1s');
await irTF('1h');
const vSuelo = await vista();
check('bajo el suelo, el cambio ensancha en vez de dejar una vela',
  vSuelo.velas >= 18, `${Math.round(vSuelo.velas)} velas`);

// 15.5 — velas nuevas por la derecha con la ventana en el pasado
await irTF('1h');
await page.evaluate((v) => window.__test.loadTF(window.__test.getTF(), { view: v }),
  { from: dias(100), to: dias(109) });
await page.waitForTimeout(2500);
const antesRueda = await vista();
const nAntes = await page.evaluate(() => window.__test.getBars().length);
await page.mouse.move(700, 400);
for (let i = 0; i < 60; i++) await page.mouse.wheel(600, 0);   // desplazar al futuro
await page.waitForTimeout(4000);
const trasRueda = await vista();
const nTras = await page.evaluate(() => window.__test.getBars().length);
const pxRueda = await page.evaluate(`(${readPixels.toString()})()`);
check('desplazarse hacia el presente carga velas nuevas por la derecha',
  nTras > nAntes && trasRueda.from > antesRueda.from,
  `${nAntes} → ${nTras} velas · ${iso(antesRueda.from)} → ${iso(trasRueda.from)}`);
check('y no deja la pantalla vacía', pxRueda.up + pxRueda.down > 1000,
  `${pxRueda.up + pxRueda.down} px de vela`);

// 15.6 — flechas del teclado (F4-1.2): conservan el ANCHO DE VELA, no el
// tramo. Bajar de temporalidad acerca y subir aleja, pegando el borde derecho
// al presente si es ahí donde se está mirando.
await irTF('1h');
await page.keyboard.press('End');
await page.waitForFunction(() => window.__test.isLive() === true, { timeout: 20000 });
await page.waitForTimeout(1500);
const vAhora0 = await vista();
await page.keyboard.press('ArrowDown');
await page.waitForFunction(() => window.__test.getTF().name === '45m', { timeout: 20000 });
await page.waitForTimeout(1500);
const vAhora1 = await vista();
check('flecha abajo baja al timeframe anterior', true, '1h → 45m');
check('y mantiene el mismo número de velas a la vista (mismo ancho de vela)',
  Math.abs(vAhora1.velas - vAhora0.velas) < 2,
  `${vAhora0.velas.toFixed(1)} → ${vAhora1.velas.toFixed(1)} velas`);
check('acercando de verdad: se ve MENOS tiempo',
  (vAhora1.to - vAhora1.from) < (vAhora0.to - vAhora0.from) * 0.85,
  `${((vAhora0.to - vAhora0.from) / 3600).toFixed(0)} h → ${((vAhora1.to - vAhora1.from) / 3600).toFixed(0)} h`);
check('y el borde derecho sigue pegado al presente',
  Math.abs(vAhora1.to - vAhora0.to) <= 2 * segs('1h'),
  `${iso(vAhora0.to)} → ${iso(vAhora1.to)}`);
await page.keyboard.press('ArrowUp');
await page.waitForFunction(() => window.__test.getTF().name === '1h', { timeout: 20000 });
await page.waitForTimeout(1500);
const vAhora2 = await vista();
check('flecha arriba sube al siguiente y aleja',
  Math.abs(vAhora2.velas - vAhora0.velas) < 2
  && (vAhora2.to - vAhora2.from) > (vAhora1.to - vAhora1.from) * 1.15,
  `${vAhora2.velas.toFixed(1)} velas · ${((vAhora2.to - vAhora2.from) / 3600).toFixed(0)} h`);

// mirando el pasado, el zoom se hace alrededor del CENTRO de la pantalla
await page.evaluate((v) => window.__test.loadTF(window.__test.getTF(), { view: v }),
  { from: dias(150), to: dias(169) });
await page.waitForTimeout(2500);
const vPasado0 = await vista();
await page.keyboard.press('ArrowDown');
await page.waitForFunction(() => window.__test.getTF().name === '45m', { timeout: 20000 });
await page.waitForTimeout(1800);
const vPasado1 = await vista();
const centroDe = (r) => (r.from + r.to) / 2;
check('en el pasado la flecha también mantiene el ancho de vela',
  Math.abs(vPasado1.velas - vPasado0.velas) < 2,
  `${vPasado0.velas.toFixed(1)} → ${vPasado1.velas.toFixed(1)} velas`);
check('y acerca alrededor del centro de lo que se está mirando',
  Math.abs(centroDe(vPasado1) - centroDe(vPasado0)) < 2 * segs('1h')
  && (vPasado1.to - vPasado1.from) < (vPasado0.to - vPasado0.from) * 0.85,
  `centro ${iso(centroDe(vPasado0))} → ${iso(centroDe(vPasado1))}, ` +
  `${((vPasado0.to - vPasado0.from) / 3600).toFixed(0)} h → ${((vPasado1.to - vPasado1.from) / 3600).toFixed(0)} h`);

// y el click en la barra sigue conservando el TRAMO, que es lo contrario
const vClick0 = await vista();
await irTF('15m');
const vClick1 = await vista();
check('el click en la barra sigue conservando el tramo (no el ancho de vela)',
  mismoRango(vClick0, vClick1, '45m', '15m') && vClick1.velas > vClick0.velas * 2,
  `${iso(vClick1.from)}…${iso(vClick1.to)} con ${vClick1.velas.toFixed(0)} velas`);

await irTF('1s');
await page.keyboard.press('ArrowDown');
await page.waitForTimeout(1200);
check('en el extremo inferior la flecha no hace nada',
  (await page.evaluate(() => window.__test.getTF().name)) === '1s');
await irTF('12M');
await page.keyboard.press('ArrowUp');
await page.waitForTimeout(1200);
check('en el extremo superior tampoco',
  (await page.evaluate(() => window.__test.getTF().name)) === '12M');

// 15.6b — con el foco en un control, las flechas son del control
await irTF('1h');
await page.evaluate(() => {
  const { engine, getBars } = window.__test;
  const b = getBars();
  engine.addShape('hline', [{ t: b[b.length - 30][0], p: b[b.length - 1][4] }], { id: 'zz-foco' });
  engine.select('zz-foco');
});
await page.waitForTimeout(400);
await page.focus('#drawPanel [data-k="opacity"]');
const op0 = await page.evaluate(() => document.querySelector('#drawPanel [data-k="opacity"]').value);
await page.keyboard.press('ArrowDown');
await page.waitForTimeout(600);
const op1 = await page.evaluate(() => document.querySelector('#drawPanel [data-k="opacity"]').value);
check('con el foco en un deslizador, la flecha lo mueve a él y no al timeframe',
  op1 !== op0 && (await page.evaluate(() => window.__test.getTF().name)) === '1h',
  `opacidad ${op0} → ${op1}`);
await page.evaluate(() => {
  window.__test.engine.remove('zz-foco');
  fetch('/api/drawings/zz-foco', { method: 'DELETE' });
});

// 15.8 — legend OHLC (F4-3.2): lo que dice tiene que ser la vela de debajo
await irTF('1m');
await page.keyboard.press('End');
await page.waitForTimeout(1500);
await page.mouse.move(700, 400);
await page.waitForTimeout(500);
const leg = await page.evaluate(() => {
  const ts = window.__test.chart.timeScale();
  const r = window.__test.engine.paneRect();
  // La vela de debajo según LWC (coordinateToLogical redondea a la más
  // cercana, que es justo a la que engancha el crosshair).
  const i = ts.coordinateToLogical(700 - r.left);
  const b = window.__test.getBars()[i];
  const txt = document.getElementById('legend').textContent;
  const span = document.querySelector('#legend .var');
  const num = (s) => parseFloat(s.replace(/\./g, '').replace(',', '.'));
  const m = txt.match(/O ([\d.,]+)\s+H ([\d.,]+)\s+L ([\d.,]+)\s+C ([\d.,]+)/);
  return { bar: b, leidos: m && m.slice(1).map(num), txt, color: span && getComputedStyle(span).color,
    prevClose: window.__test.getBars()[i - 1] && window.__test.getBars()[i - 1][4] };
});
check('la legend muestra el OHLC de la vela que hay bajo el cursor',
  leg.leidos && Math.abs(leg.leidos[0] - leg.bar[1]) < 0.011 && Math.abs(leg.leidos[1] - leg.bar[2]) < 0.011
  && Math.abs(leg.leidos[2] - leg.bar[3]) < 0.011 && Math.abs(leg.leidos[3] - leg.bar[4]) < 0.011,
  `${leg.txt} · vela [${leg.bar.slice(1, 5).join(', ')}]`);
const subeVela = leg.bar[4] >= leg.prevClose;
check('y la variación va con la paleta del usuario, no en verde/rojo',
  leg.color === (subeVela ? 'rgb(112, 146, 190)' : 'rgb(218, 218, 218)'),
  `${leg.color} (${subeVela ? 'sube' : 'baja'})`);
await page.mouse.move(20, 780);   // fuera del gráfico
await page.waitForTimeout(600);
const legFuera = await page.evaluate(() => {
  const b = window.__test.getBars().at(-1);
  const num = (s) => parseFloat(s.replace(/\./g, '').replace(',', '.'));
  const m = document.getElementById('legend').textContent.match(/C ([\d.,]+)/);
  return { cierre: b[4], leido: m && num(m[1]) };
});
check('sin cursor encima, la legend enseña la última vela',
  Math.abs(legFuera.leido - legFuera.cierre) < 0.011, `${legFuera.leido} vs ${legFuera.cierre}`);

// 15.7 — el estado del gráfico sobrevive a la recarga (F4-1.3)
await irTF('30m');
await page.evaluate((v) => window.__test.loadTF(window.__test.getTF(), { view: v }),
  { from: dias(200), to: dias(205) });
await page.waitForTimeout(2500);
const vAntesRecarga = await vista();
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await page.waitForTimeout(2000);
const vTrasRecarga = await vista();
check('la recarga vuelve al mismo timeframe',
  (await page.evaluate(() => window.__test.getTF().name)) === '30m');
check('y a la misma posición', mismoRango(vAntesRecarga, vTrasRecarga, '30m', '30m'),
  `${iso(vAntesRecarga.from)}…${iso(vAntesRecarga.to)} → ${iso(vTrasRecarga.from)}…${iso(vTrasRecarga.to)}`);
await page.evaluate(() => localStorage.removeItem(window.__test.VIEW_KEY));

// ============================================================ F5 · alertas
// El motor vive en el servidor y se prueba aparte (Go, contra la BD de test).
// Aquí se comprueba la interfaz: crear con el botón derecho, ver la línea
// pintada, listarla y borrarla.
// Cuenta píxeles ámbar (el color de las alertas) en el lienzo del gráfico.
const pixelesAmbarEn = () => page.evaluate(() => {
  const cs = [...document.querySelectorAll('#chart canvas')];
  const area = Math.max(...cs.map(c => c.width * c.height));
  let n = 0;
  for (const c of cs) {
    if (c.width * c.height < area * 0.9) continue;
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i] > 180 && d[i + 1] > 110 && d[i + 1] < 200 && d[i + 2] < 100) n++;
    }
  }
  return n;
});

await irTF('1m');
await page.keyboard.press('End');
await page.waitForTimeout(1500);
await page.evaluate(async () => {
  for (const a of await (await fetch('/api/alerts')).json()) {
    await fetch(`/api/alerts/${a.id}`, { method: 'DELETE' });
  }
  await window.__test.alertas.recargar();
});
await page.waitForTimeout(400);
// precio bajo el cursor según LWC, para comparar con lo que se guarda
const puntoAlerta = { x: 700, y: 380 };
const precioEsperado = await page.evaluate((p) => {
  const r = window.__test.engine.paneRect();
  return window.__test.series.coordinateToPrice(p.y - r.top);
}, puntoAlerta);

await page.mouse.click(puntoAlerta.x, puntoAlerta.y, { button: 'right' });
await page.waitForTimeout(400);
const menuVisible = await page.evaluate(() => {
  const m = document.getElementById('alertMenu');
  return m && !m.hidden ? m.textContent : null;
});
check('el botón derecho abre el menú de alerta con el precio de ese punto',
  !!menuVisible && menuVisible.includes('Alerta en'), menuVisible);

await page.evaluate(() => [...document.querySelectorAll('#alertMenu button')]
  .find(b => b.textContent.includes('cualquier')).click());
await page.waitForTimeout(900);
const creadas = await page.evaluate(() => fetch('/api/alerts').then(r => r.json()));
check('crear la alerta la guarda en el servidor con el nivel del punto',
  creadas.length === 1 && Math.abs(creadas[0].level - precioEsperado) < precioEsperado * 0.002,
  `nivel ${creadas[0]?.level?.toFixed(2)} vs precio ${precioEsperado.toFixed(2)}`);
check('y con los valores por defecto (cualquier cruce, una vez, armada)',
  creadas[0].direction === 'any' && creadas[0].mode === 'once' && creadas[0].status === 'armed',
  JSON.stringify({ d: creadas[0]?.direction, m: creadas[0]?.mode, s: creadas[0]?.status }));

await page.mouse.move(20, 780);
await page.waitForTimeout(600);
const pixelesAmbar = await pixelesAmbarEn();
check('la alerta se ve en el gráfico como una línea', pixelesAmbar > 200, `${pixelesAmbar} px ámbar`);

// el panel la lista y la borra
await page.click('button[data-tool="__alerts"]');
await page.waitForTimeout(600);
const enPanel = await page.evaluate(() => {
  const p = document.getElementById('alertPanel');
  return { visible: !p.hidden, filas: p.querySelectorAll('.alerta').length,
    texto: p.querySelector('.alerta b')?.textContent };
});
check('el panel lista la alerta', enPanel.visible && enPanel.filas === 1, JSON.stringify(enPanel));

await page.evaluate(() => document.querySelector('#alertPanel .alerta button:last-child').click());
await page.waitForTimeout(900);
const trasBorrar = await page.evaluate(() => fetch('/api/alerts').then(r => r.json()));
const pixelesTrasBorrar = await pixelesAmbarEn();
check('borrarla la quita del servidor y del gráfico',
  trasBorrar.length === 0 && pixelesTrasBorrar < pixelesAmbar / 4,
  `${trasBorrar.length} alertas · ${pixelesAmbar} → ${pixelesTrasBorrar} px`);
await page.evaluate(() => { document.getElementById('alertPanel').hidden = true; });

await browser.close();
console.log(fails.length ? `\nFALLOS: ${fails.join(', ')}` : '\nTODO OK');
process.exit(fails.length ? 1 : 0);
