// Suite de dibujos (F3) con GESTOS REALES: mousedown / mousemove / mouseup a
// coordenadas concretas. Nada de "existe el método drag": cada comprobación
// mira el efecto — el modelo movido, el rango del gráfico intacto y los
// píxeles pintados en el canvas.
//
// Uso: node draw-test.mjs   (API en 127.0.0.1:8090 sirviendo web/dist)
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
const wait = (ms) => page.waitForTimeout(ms);

// La base de datos es la MISMA que usa el usuario (el API local va por el túnel
// contra jordios): esta suite borra la tabla de dibujos para trabajar en
// limpio, así que primero se guarda lo que haya y al terminar se repone. Un
// PUT con el mismo id es upsert, o sea que la restauración es exacta salvo el
// updated_at. Se repone también si la suite se cae a media ejecución.
const API = 'http://127.0.0.1:8090';
const previos = await (await fetch(`${API}/api/drawings`)).json();
if (previos.length) console.log(`(guardados ${previos.length} dibujos del usuario para reponerlos al final)`);
async function restaurar() {
  for (const d of await (await fetch(`${API}/api/drawings`)).json()) {
    await fetch(`${API}/api/drawings/${d.id}`, { method: 'DELETE' });
  }
  for (const d of previos) {
    await fetch(`${API}/api/drawings/${d.id}`, { method: 'PUT',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(d.payload) });
  }
  if (previos.length) console.log(`(repuestos ${previos.length} dibujos del usuario)`);
}
for (const ev of ['uncaughtException', 'unhandledRejection']) {
  process.on(ev, async (e) => { console.error(e); await restaurar(); process.exit(1); });
}

await page.goto('http://127.0.0.1:8090/');
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(600);

// limpieza previa: la BD es compartida con el usuario
await page.evaluate(async () => {
  window.__test.engine.clear();
  for (const d of await (await fetch('/api/drawings')).json()) {
    await fetch(`/api/drawings/${d.id}`, { method: 'DELETE' });   // restos de sesiones previas
  }
});
await wait(300);

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
    velas: window.__test.getBars().length,
    panel: !document.getElementById('drawPanel').hidden,
  };
});

// El gráfico "no se ha movido" cuando su rango lógico solo se ha desplazado lo
// que entraron velas nuevas: LWC guarda la posición como distancia a la última
// vela (trampa 15), así que cada vela en vivo suma 1 al rango sin que nadie
// haya paneado. Sin esto, un minuto redondo a mitad de gesto rompía el test.
const noSeMovio = (a, b) => {
  const nuevas = b.velas - a.velas;
  return Math.abs(b.rango.from - nuevas - a.rango.from) < 0.02
    && Math.abs(b.rango.to - nuevas - a.rango.to) < 0.02;
};

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
  ['text', [[300, 200]]],
  ['zone2', [[600, 620], [640, 660]]],
];
for (const [tool, pts] of HERRAMIENTAS) await crear(tool, pts);
const creado = await estado();
check('las 8 figuras se crean con clicks reales',
  creado.n === HERRAMIENTAS.length && HERRAMIENTAS.every(([t]) => creado.tipos.includes(t)),
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
check('arrastrar NO mueve el gráfico', noSeMovio(antes, despues),
  `${JSON.stringify(antes.rango)} → ${JSON.stringify(despues.rango)} (+${despues.velas - antes.velas} velas)`);
// y el segundo punto se movió lo mismo: la figura se traslada entera
const d2T = rectDespues.pts[1].t - rectAntes.pts[1].t;
check('la figura se traslada entera', Math.abs(d2T - dT) <= 1, `p0 ${dT}s vs p1 ${d2T}s`);

// ------------------------- 2b. arrastre de CADA tipo de figura, uno a uno
// El fallo reportado era "solo se ajusta en vertical y el gráfico se mueve".
// Se comprueba tipo por tipo, con el lienzo limpio para que no haya dudas de
// qué figura se agarra.
const limpiar = async () => {
  await page.evaluate(() => window.__test.engine.clear());   // cancela guardados y borra
  await wait(300);
};
// Punto por el que agarrar cada tipo (en coordenadas de pantalla).
const agarre = () => page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes[0];
  const pts = e.screenPoints(s);
  const r = e.paneRect();
  const mid = (a, b) => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });
  let q;
  switch (s.type) {
    case 'hline': q = { x: 300, y: pts[0].y }; break;
    case 'hray': q = { x: pts[0].x + 120, y: pts[0].y }; break;
    case 'zone2': q = { x: pts[0].x + 120, y: (pts[0].y + pts[1].y) / 2 }; break;
    case 'trend': q = mid(pts[0], pts[1]); break;
    case 'rect': q = mid(pts[0], pts[1]); break;
    case 'curve': {   // Bézier en t=0.5
      const b = (a, bb, c, d) => 0.125 * a + 0.375 * bb + 0.375 * c + 0.125 * d;
      q = { x: b(pts[0].x, pts[1].x, pts[2].x, pts[3].x), y: b(pts[0].y, pts[1].y, pts[2].y, pts[3].y) };
      break;
    }
    case 'arc': {   // un punto DEL arco lejos de los tres puntos de control:
                    // agarrar justo encima de uno redimensionaría, no movería
      const c = (a, bb, cc) => {
        const d = 2 * (a.x * (bb.y - cc.y) + bb.x * (cc.y - a.y) + cc.x * (a.y - bb.y));
        const a2 = a.x * a.x + a.y * a.y, b2 = bb.x * bb.x + bb.y * bb.y, c2 = cc.x * cc.x + cc.y * cc.y;
        const cx = (a2 * (bb.y - cc.y) + b2 * (cc.y - a.y) + c2 * (a.y - bb.y)) / d;
        const cy = (a2 * (cc.x - bb.x) + b2 * (a.x - cc.x) + c2 * (bb.x - a.x)) / d;
        return { cx, cy, r: Math.hypot(a.x - cx, a.y - cy) };
      };
      const k = c(pts[0], pts[1], pts[2]);
      const ang = (pp) => Math.atan2(pp.y - k.cy, pp.x - k.cx);
      const a0 = ang(pts[0]), a1 = ang(pts[1]);
      const m = a0 + (a1 - a0) / 2;               // entre el primero y el medio
      q = { x: k.cx + k.r * Math.cos(m), y: k.cy + k.r * Math.sin(m) };
      break;
    }
    case 'text': q = { x: pts[0].x + 12, y: pts[0].y }; break;
    default: q = pts[0];
  }
  return { x: r.left + q.x, y: r.top + q.y };
});

for (const [tool, pts] of HERRAMIENTAS) {
  await limpiar();
  await crear(tool, pts);
  let g = await agarre();
  await page.mouse.click(g.x, g.y);              // seleccionar
  await wait(250);
  let a0 = await estado();
  if (a0.sel === null) {
    // La escala de precio se reajusta con cada vela en vivo y el punto de
    // agarre calculado hace 250 ms puede haberse movido unos píxeles: se
    // recalcula y se reintenta antes de dar el fallo por bueno.
    g = await agarre();
    await page.mouse.click(g.x, g.y);
    await wait(250);
    a0 = await estado();
  }
  if (a0.sel === null) { check(`${tool}: se puede seleccionar con click`, false, 'no seleccionó'); continue; }
  g = await agarre();          // recalculado justo antes: la escala se mueve sola
  await arrastrar(g.x, g.y, g.x + 130, g.y - 55);
  const a1 = await estado();
  const p0 = a0.figuras[0].pts[0], p1 = a1.figuras[0].pts[0];
  const movX = p1.t - p0.t, movY = p1.p - p0.p;
  check(`${tool}: se arrastra en los DOS ejes y el gráfico no se mueve`,
    movX > 0 && movY > 0 && noSeMovio(a0, a1),
    `Δt=${movX}s Δp=${movY.toFixed(2)} · rango ${a0.rango.from}→${a1.rango.from} (+${a1.velas - a0.velas} velas)`);
}
await limpiar();
for (const [tool, pts] of HERRAMIENTAS) await crear(tool, pts);   // repuebla para lo que sigue
await page.mouse.click(825, 360);
await wait(300);

// ---------------------------- 2c. control espejo: el gráfico SÍ se panea
// La suite demuestra que arrastrar un dibujo no mueve el gráfico; hace falta
// lo contrario, o un candado pegado (handleScroll:false) pasaría inadvertido.
const rangoOriginal = (await estado()).rango;
const panear = async () => {
  // punto de agarre garantizado sin figuras debajo (las hay repartidas)
  const punto = await page.evaluate(() => {
    const e = window.__test.engine, r = e.paneRect();
    for (let y = r.height - 40; y > 60; y -= 15) {
      if (![500, 700, 900, 1100].some(x => e._shapeAt(x, y))) return { x: r.left + 1100, y: r.top + y };
    }
    return null;
  });
  if (!punto) return { movido: false, nota: 'sin hueco libre' };
  const r0 = (await estado()).rango;
  await arrastrar(punto.x, punto.y, punto.x - 200, punto.y);
  const r1 = (await estado()).rango;
  return { r0, r1, movido: Math.abs(r1.from - r0.from) > 1 };
};
const trasCrear = await panear();
check('arrastrar el fondo SÍ panea el gráfico tras crear figuras',
  trasCrear.movido, `${trasCrear.r0?.from} → ${trasCrear.r1?.from}`);
check('handleScroll queda restaurado',
  await page.evaluate(() => window.__test.chart.options().handleScroll.pressedMouseMove === true));
await page.keyboard.down('Shift'); await page.mouse.click(600, 300); await page.keyboard.up('Shift');
await wait(200);
await page.mouse.click(700, 350); await wait(200);   // fija la medición
await page.mouse.click(700, 350); await wait(200);   // la borra
const trasMedir = await panear();
check('arrastrar el fondo SÍ panea el gráfico tras medir', trasMedir.movido,
  `${trasMedir.r0?.from} → ${trasMedir.r1?.from}`);
// deja la vista como estaba: lo que sigue usa coordenadas fijas
await page.evaluate((r) => window.__test.chart.timeScale().setVisibleLogicalRange(r), rangoOriginal);
await wait(500);
const centroRect0 = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(centroRect0.x, centroRect0.y);   // lo que sigue lo espera seleccionado
await wait(300);

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
const centroRect = await page.evaluate(() => {   // el rectángulo, donde esté ahora
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(centroRect.x, centroRect.y);
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

// ------------------- 4b. lados del rectángulo: un solo eje cada uno (F4b)
// Como en TradingView: los centros de lado ajustan alto o ancho sin tocar el
// otro anclaje. Antes solo había esquinas y no se podía estirar en un eje.
const centroRect2 = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(centroRect2.x, centroRect2.y);
await wait(300);
const lados = () => page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const pts = e.screenPoints(s), r = e.paneRect();
  const mx = (pts[0].x + pts[1].x) / 2, my = (pts[0].y + pts[1].y) / 2;
  return { arriba: { x: r.left + mx, y: r.top + pts[0].y },
           derecha: { x: r.left + pts[1].x, y: r.top + my } };
});
const l0 = await lados();
const rA = (await estado()).figuras.find(f => f.type === 'rect');
await arrastrar(l0.arriba.x, l0.arriba.y, l0.arriba.x + 60, l0.arriba.y + 45);   // se mueve también en X
const rB = (await estado()).figuras.find(f => f.type === 'rect');
check('el lado de arriba cambia el ALTO y no toca el ancho',
  Math.abs(rB.pts[0].p - rA.pts[0].p) > 1 && Math.abs(rB.pts[1].p - rA.pts[1].p) < 0.01
  && rB.pts[0].t === rA.pts[0].t && rB.pts[1].t === rA.pts[1].t,
  `Δp0=${(rB.pts[0].p - rA.pts[0].p).toFixed(2)} Δp1=${(rB.pts[1].p - rA.pts[1].p).toFixed(2)} · Δt0=${rB.pts[0].t - rA.pts[0].t}s Δt1=${rB.pts[1].t - rA.pts[1].t}s`);
const l1 = await lados();
await arrastrar(l1.derecha.x, l1.derecha.y, l1.derecha.x + 90, l1.derecha.y - 50);
const rC = (await estado()).figuras.find(f => f.type === 'rect');
check('el lado derecho cambia el ANCHO y no toca el alto',
  rC.pts[1].t > rB.pts[1].t && rC.pts[0].t === rB.pts[0].t
  && Math.abs(rC.pts[0].p - rB.pts[0].p) < 0.01 && Math.abs(rC.pts[1].p - rB.pts[1].p) < 0.01,
  `Δt1=${rC.pts[1].t - rB.pts[1].t}s · Δp0=${(rC.pts[0].p - rB.pts[0].p).toFixed(2)} Δp1=${(rC.pts[1].p - rB.pts[1].p).toFixed(2)}`);
const tiradores = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  const p = e.screenPoints(s);
  const mx = (p[0].x + p[1].x) / 2, my = (p[0].y + p[1].y) / 2;
  return [[p[0].x, p[0].y], [p[1].x, p[0].y], [p[1].x, p[1].y], [p[0].x, p[1].y],
    [mx, p[0].y], [p[1].x, my], [mx, p[1].y], [p[0].x, my]]
    .map(([x, y]) => e._handleAt(x, y)).filter(i => i >= 0).length;
});
check('el rectángulo tiene ocho tiradores: cuatro esquinas y cuatro lados',
  tiradores === 8, `${tiradores}/8 agarrables`);

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
// borrar ANTES de que venza el guardado diferido no puede resucitar la figura
// en la siguiente recarga (el debounce escribía después del DELETE).
const antesFugaz = (await estado()).n;
await page.click('button[data-tool="hline"]');
await page.mouse.click(430, 330);
await page.keyboard.press('Delete');
await wait(1200);
const trasFugaz = await page.evaluate(() => fetch('/api/drawings').then(r => r.json()));
const nTrasFugaz = (await estado()).n;
check('borrar antes del debounce no resucita la figura',
  trasFugaz.length === antesFugaz && nTrasFugaz === antesFugaz,
  `${trasFugaz.length} filas en la API, ${antesFugaz} esperadas`);

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
// Efecto, no intención: dónde PINTA el motor el primer punto frente a dónde
// dice LWC que cae ese instante (camino independiente, timeToCoordinate sobre
// la vela más cercana). Si el mapeo tiempo→x estuviera roto, no cuadrarían.
const cotejo = await page.evaluate((t) => {
  const { engine, chart, getBars } = window.__test;
  const s = engine.shapes.find(x => x.type === 'trend');
  const pintado = engine.screenPoints(s)?.[0]?.x ?? null;
  const bars = getBars();
  let cerca = bars[0];
  for (const b of bars) if (Math.abs(b[0] - t) < Math.abs(cerca[0] - t)) cerca = b;
  const segunLWC = chart.timeScale().timeToCoordinate(cerca[0]);
  // La referencia está CUANTIZADA a la vela más cercana, así que la
  // tolerancia es el ancho de vela EN PANTALLA (no el medio del histórico
  // cargado: con pocas velas a la vista una sola ocupa decenas de píxeles).
  // Si el mapeo tiempo→x estuviera roto el error sería de cientos de píxeles
  // — el fallo de la trampa 14 pintaba en x=0.
  const ts = chart.timeScale();
  const i = ts.coordinateToLogical(pintado ?? 0);
  const espaciado = Math.abs((ts.logicalToCoordinate(i + 1) ?? 0) - (ts.logicalToCoordinate(i) ?? 0));
  return { pintado, segunLWC, tolerancia: Math.max(4, espaciado), t, tBarra: cerca[0] };
}, antesTF.pts[0].t);
check('tras cambiar de timeframe el dibujo se pinta donde toca',
  cotejo.pintado !== null && cotejo.segunLWC !== null
  && Math.abs(cotejo.pintado - cotejo.segunLWC) <= cotejo.tolerancia,
  `pintado en x=${cotejo.pintado?.toFixed(1)}, LWC dice x=${cotejo.segunLWC?.toFixed(1)} (tol ${cotejo.tolerancia?.toFixed(1)})`);
const mismoPrecio = await page.evaluate((p) => {
  const { engine, series } = window.__test;
  const s = engine.shapes.find(x => x.type === 'trend');
  return { y: engine.screenPoints(s)?.[0]?.y, yEsperada: series.priceToCoordinate(p) };
}, antesTF.pts[0].p);
check('y a la altura de su precio', Math.abs(mismoPrecio.y - mismoPrecio.yEsperada) < 1,
  `y=${mismoPrecio.y?.toFixed(1)} esperada=${mismoPrecio.yEsperada?.toFixed(1)}`);
await page.click('button[data-tf="1m"]');
await page.waitForFunction(() => window.__test.getTF().name === '1m' && window.__test.getBars().length > 100, { timeout: 20000 });
await wait(600);

// ------------------------- 7c. modos que no se quedan pegados (revisión F3)
await page.click('button[data-tool="__measure"]');
await wait(150);
const armado = await page.evaluate(() => window.__test.engine.armed === true);
await page.click('button[data-tool="trend"]');
await wait(150);
const trasElegirHerramienta = await page.evaluate(() => window.__test.engine.armed === true);
await page.keyboard.press('Escape');
await wait(150);
check('elegir herramienta cancela el modo medir',
  armado && trasElegirHerramienta === false, `armado=${armado} tras herramienta=${trasElegirHerramienta}`);
await page.click('button[data-tool="__measure"]');
await wait(150);
await page.keyboard.press('Escape');
await wait(150);
check('Escape sale del modo medir',
  await page.evaluate(() => window.__test.engine.armed === false));

// un gesto que muere sin pointerup no puede dejar la figura pegada al ratón
const antesZombi = (await estado()).figuras.find(f => f.type === 'trend');
const gTrend = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'trend');
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(gTrend.x, gTrend.y);
await wait(200);
await page.mouse.move(gTrend.x, gTrend.y);
await page.mouse.down();
await page.mouse.move(gTrend.x + 30, gTrend.y + 10);
await page.evaluate(() => dispatchEvent(new PointerEvent('pointercancel', { bubbles: true })));
await wait(200);
const dragMuerto = await page.evaluate(() => window.__test.engine.drag === null);
const trasCancelar = (await estado()).figuras.find(f => f.type === 'trend');
await page.mouse.move(gTrend.x + 300, gTrend.y + 150);   // sin botón: no debe arrastrar
await wait(300);
const trasMover = (await estado()).figuras.find(f => f.type === 'trend');
await page.mouse.up();
check('un arrastre cancelado no deja la figura pegada al ratón',
  dragMuerto && Math.abs(trasMover.pts[0].p - trasCancelar.pts[0].p) < 1e-9
  && trasMover.pts[0].t === trasCancelar.pts[0].t,
  `drag=${dragMuerto ? 'null' : 'vivo'} · Δp tras mover=${(trasMover.pts[0].p - trasCancelar.pts[0].p).toFixed(4)}`);
await page.keyboard.press('Escape');
await wait(200);

// -------------------------------------------------------------- 8. medición
await page.keyboard.press('Escape');
await wait(200);
const estadoAntesMedir = await estado();
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
check('la medición no desplaza el gráfico', noSeMovio(estadoAntesMedir, await estado()));
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

// ------------------------- 8b. robustez: una fila corrupta no rompe el resto
await page.evaluate(() => fetch('/api/drawings/zz-corrupta', {
  method: 'PUT', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ kind: 'shape', v: 1, type: 'hline',
    points: [{ t: null, p: null }], style: { color: '#ff0000', width: 2, opacity: 1, fill: '#ff0000', fillOpacity: 0.1, dash: false } }),
}));
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(800);
const conBasura = await page.evaluate(() => ({
  figuras: window.__test.engine.shapes.length,
  velas: window.__test.getBars().length,
  tipos: window.__test.engine.shapes.map(s => s.type),
}));
check('una fila corrupta se ignora y el gráfico sigue vivo',
  conBasura.velas > 100 && !conBasura.tipos.includes(undefined) && conBasura.figuras > 0,
  `${conBasura.figuras} figuras cargadas, ${conBasura.velas} velas`);
const pxTrasBasura = await pixeles();
check('y el lienzo se sigue pintando', Object.keys(pxTrasBasura).length > 3);
await page.evaluate(() => fetch('/api/drawings/zz-corrupta', { method: 'DELETE' }));

// el crosshair no puede quedar tapado por un relleno opaco
await crear('rect', [[500, 300], [900, 500]]);
await page.evaluate(() => {
  const e = window.__test.engine;
  const r = e.shapes.find(s => s.type === 'rect');
  if (r) { r.style.fill = '#00ff00'; r.style.fillOpacity = 1; e.redraw(); }
});
const centro = await page.evaluate(() => {
  const e = window.__test.engine;
  const s = e.shapes.find(x => x.type === 'rect');
  if (!s) return null;
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
if (centro) {
  await page.mouse.move(centro.x, centro.y);
  await wait(400);
  const px = await pixeles();
  check('el crosshair se ve por encima de un relleno opaco', (px['30,30,30'] || 0) > 100,
    `${px['30,30,30'] || 0} px de crosshair`);
}


// ------------------------------------------- F4-2. estilo por defecto y plantillas
await limpiar();
const estiloDe = (i = 0) => page.evaluate((k) => {
  const s = window.__test.engine.shapes[k];
  return s ? { ...s.style, type: s.type } : null;
}, i);
const ponEstilo = (k, v) => page.evaluate(([kk, vv]) => {
  const i = document.querySelector(`#drawPanel [data-k="${kk}"]`);
  i.value = vv; i.dispatchEvent(new Event('input', { bubbles: true }));
}, [k, v]);

// 2.1 — lo último aplicado se vuelve el defecto de esa herramienta
await crear('trend', [[400, 300], [700, 380]]);
await page.mouse.click(550, 340);
await wait(300);
await ponEstilo('color', '#00ffff');
await ponEstilo('width', '4');
await wait(400);
await limpiar();
await crear('trend', [[300, 500], [600, 560]]);
const heredado = await estiloDe();
const pxHeredado = await pixeles();
check('el último estilo aplicado se vuelve el defecto de la herramienta',
  heredado.color === '#00ffff' && heredado.width === 4, JSON.stringify(heredado));
check('y se ve pintado en el dibujo nuevo', cuenta(pxHeredado, '0,255,255') > 200,
  `${cuenta(pxHeredado, '0,255,255')} px cian`);

// y sobrevive a la recarga
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(700);
await limpiar();
await crear('trend', [[350, 450], [650, 520]]);
const trasRecargarEstilo = await estiloDe();
check('el estilo por defecto sobrevive a la recarga',
  trasRecargarEstilo.color === '#00ffff' && trasRecargarEstilo.width === 4,
  JSON.stringify(trasRecargarEstilo));

// 2.2 — plantillas: guardar, aplicar a un dibujo YA existente, marcar y borrar
const agarreTrend = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(agarreTrend.x, agarreTrend.y);
await wait(300);
await page.click('#drawPanel [data-act="tpl-save"]');
await page.fill('#drawPanel [data-k="tplName"]', 'cian gordo');
await page.click('#drawPanel [data-act="tpl-ok"]');
await wait(300);
const opciones = () => page.evaluate(() =>
  [...document.querySelectorAll('#drawPanel [data-k="tpl"] option')].map(o => o.textContent));
check('la plantilla se guarda y aparece en la lista',
  (await opciones()).some(o => o.includes('cian gordo')), JSON.stringify(await opciones()));

// se cambia el estilo del dibujo y la plantilla lo devuelve a su sitio
await ponEstilo('color', '#ff8800');
await ponEstilo('width', '1');
await wait(400);
const cambiado = await estiloDe();
await page.selectOption('#drawPanel [data-k="tpl"]', 'cian gordo');
await wait(500);
const restaurado = await estiloDe();
const pxRestaurado = await pixeles();
check('aplicar una plantilla cambia un dibujo YA existente',
  cambiado.color === '#ff8800' && restaurado.color === '#00ffff' && restaurado.width === 4,
  `${cambiado.color}/${cambiado.width} → ${restaurado.color}/${restaurado.width}`);
check('y se ve en el lienzo, no solo en el modelo', cuenta(pxRestaurado, '0,255,255') > 200,
  `${cuenta(pxRestaurado, '0,255,255')} px cian`);

// marcarla como predeterminada manda también sobre OTRAS herramientas
await ponEstilo('color', '#ff8800');            // ensucia el defecto de trend
await wait(300);
await page.click('#drawPanel [data-act="tpl-def"]');
await wait(300);
check('la predeterminada se marca en la lista',
  (await opciones()).some(o => o.startsWith('★')), JSON.stringify(await opciones()));
await limpiar();
await crear('rect', [[500, 250], [800, 400]]);
const rectNuevo = await estiloDe();
check('la plantilla predeterminada estrena las demás herramientas',
  rectNuevo.color === '#00ffff' && rectNuevo.width === 4, JSON.stringify(rectNuevo));
await limpiar();
await crear('trend', [[300, 300], [600, 360]]);
const trendTrasDefecto = await estiloDe();
check('y también las que ya se habían tocado a mano',
  trendTrasDefecto.color === '#00ffff', JSON.stringify(trendTrasDefecto));

// borrar la plantilla
await page.mouse.click(
  ...Object.values(await page.evaluate(() => {
    const e = window.__test.engine, s = e.shapes[0];
    const pts = e.screenPoints(s), r = e.paneRect();
    return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
  })));
await wait(300);
await page.selectOption('#drawPanel [data-k="tpl"]', 'cian gordo');
await wait(300);
await page.click('#drawPanel [data-act="tpl-del"]');
await wait(300);
check('la plantilla se puede borrar',
  !(await opciones()).some(o => o.includes('cian gordo')), JSON.stringify(await opciones()));
await page.evaluate(() => {
  ['btcdash.estiloActual', 'btcdash.plantillas', 'btcdash.plantillaDefecto']
    .forEach(k => localStorage.removeItem(k));
});
await limpiar();


// ------------------------------------------------------ F4-3.1 deshacer/rehacer
await limpiar();
const nFiguras = () => page.evaluate(() => window.__test.engine.shapes.length);
const enBD = () => page.evaluate(() => fetch('/api/drawings').then(r => r.json()).then(d => d.length));

// crear → deshacer → rehacer
await crear('rect', [[500, 300], [800, 450]]);
const uCrear = await estado();
await page.keyboard.press('Control+z');
await wait(500);
const trasDeshacerCrear = await nFiguras();
await page.keyboard.press('Control+Shift+z');
await wait(600);
const uRehacer = await estado();
check('Ctrl+Z deshace la creación', uCrear.n === 1 && trasDeshacerCrear === 0,
  `${uCrear.n} → ${trasDeshacerCrear}`);
check('Ctrl+Shift+Z la rehace con su sitio y su estilo',
  uRehacer.n === 1 && uRehacer.figuras[0].pts[0].t === uCrear.figuras[0].pts[0].t
  && uRehacer.figuras[0].style.color === uCrear.figuras[0].style.color,
  JSON.stringify(uRehacer.figuras[0].pts[0]));
await wait(700);
check('y el servidor se entera del deshacer', (await enBD()) === 1, `${await enBD()} filas`);

// mover → deshacer (gesto real de arrastre)
const centroR = () => page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
let c = await centroR();
await page.mouse.click(c.x, c.y);
await wait(300);
const uAntesMover = (await estado()).figuras[0].pts[0];
await arrastrar(c.x, c.y, c.x + 150, c.y - 70);
const uMover = (await estado()).figuras[0].pts[0];
await page.keyboard.press('Control+z');
await wait(600);
const trasDeshacerMover = (await estado()).figuras[0].pts[0];
check('Ctrl+Z deshace un arrastre',
  uMover.t > uAntesMover.t && Math.abs(trasDeshacerMover.t - uAntesMover.t) < 2
  && Math.abs(trasDeshacerMover.p - uAntesMover.p) < 0.01,
  `${uAntesMover.t} → ${uMover.t} → ${trasDeshacerMover.t}`);

// redimensionar → deshacer
c = await centroR();
await page.mouse.click(c.x, c.y);
await wait(300);
const handleR = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + pts[1].x, y: r.top + pts[1].y };
});
const antesRedim = (await estado()).figuras[0].pts[1];
await arrastrar(handleR.x, handleR.y, handleR.x + 100, handleR.y + 60);
const trasRedim = (await estado()).figuras[0].pts[1];
await page.keyboard.press('Control+z');
await wait(600);
const trasDeshacerRedim = (await estado()).figuras[0].pts[1];
check('Ctrl+Z deshace un redimensionado',
  Math.abs(trasRedim.t - antesRedim.t) > 1 && Math.abs(trasDeshacerRedim.t - antesRedim.t) < 2,
  `${antesRedim.t} → ${trasRedim.t} → ${trasDeshacerRedim.t}`);

// estilo → deshacer (y se ve en el lienzo)
c = await centroR();
await page.mouse.click(c.x, c.y);
await wait(300);
const colorAntes = (await estado()).figuras[0].style.color;
await ponEstilo('color', '#ff00aa');
await wait(900);                                   // el estilo se agrupa 500 ms
const pxRosa = cuenta(await pixeles(), '255,0,170');
await page.keyboard.press('Control+z');
await wait(700);
const colorTras = (await estado()).figuras[0].style.color;
const pxRosaTras = cuenta(await pixeles(), '255,0,170');
check('Ctrl+Z deshace un cambio de estilo y se ve en el lienzo',
  colorTras === colorAntes && pxRosa > 100 && pxRosaTras === 0,
  `${colorAntes} → #ff00aa (${pxRosa} px) → ${colorTras} (${pxRosaTras} px)`);

// borrar → deshacer
c = await centroR();
await page.mouse.click(c.x, c.y);
await wait(300);
await page.keyboard.press('Delete');
await wait(400);
const trasBorrarUndo = await nFiguras();
await page.keyboard.press('Control+z');
await wait(600);
check('Ctrl+Z deshace un borrado', trasBorrarUndo === 0 && (await nFiguras()) === 1,
  `${trasBorrarUndo} → ${await nFiguras()}`);
// Ctrl+Y como alternativa a Ctrl+Shift+Z
await page.keyboard.press('Control+z');
await wait(500);
await page.keyboard.press('Control+y');
await wait(600);
check('Ctrl+Y rehace igual que Ctrl+Shift+Z', (await nFiguras()) === 1);

// profundidad: 55 pasos hacia atrás
await limpiar();
await page.evaluate(() => {
  const { engine, getBars } = window.__test;
  const b = getBars();
  for (let i = 0; i < 55; i++) {
    engine.addShape('hline', [{ t: b[b.length - 200 + i][0], p: b[b.length - 1][4] * (1 + i / 1000) }]);
  }
});
await wait(400);
const antesPila = await nFiguras();
for (let i = 0; i < 52; i++) { await page.keyboard.press('Control+z'); await wait(30); }
await wait(800);
const trasPila = await nFiguras();
check('el historial aguanta más de 50 pasos', antesPila === 55 && trasPila === 3,
  `${antesPila} → ${trasPila} figuras tras 52 deshacer`);
await limpiar();
await wait(600);


// -------------------------------------------------------------- F4-3.3 imán
await limpiar();
// Un objetivo concreto: el máximo de una vela a la vista, con sus coordenadas
// según LWC (timeToCoordinate / priceToCoordinate), no según el motor.
const medirDiana = () => page.evaluate(() => {
  const { chart, series, engine, getBars } = window.__test;
  const ts = chart.timeScale();
  const r = ts.getVisibleLogicalRange();
  const b = getBars();
  const rect = engine.paneRect();
  // Una vela cuyo máximo sea el más alto de su entorno: así el extremo más
  // cercano a un clic 7 px por encima solo puede ser ESE máximo. Con una vela
  // cualquiera, la apertura de la de al lado puede quedar más cerca y el imán
  // haría bien enganchándose a ella, pero el test se quejaría sin razón.
  // Además tiene que caer en una zona despejada de la pantalla: la barra de
  // herramientas flota sobre la esquina superior izquierda y se comería el clic.
  for (let i = Math.max(2, Math.ceil(r.from) + 2); i <= Math.min(b.length - 3, Math.floor(r.to) - 2); i++) {
    const alto = b[i][2];
    if (![-2, -1, 1, 2].every(k => b[i + k][2] < alto)) continue;
    const x = rect.left + ts.timeToCoordinate(b[i][0]);
    const y = rect.top + series.priceToCoordinate(alto);
    if (x < 500 || x > rect.right - 60 || y < 150 || y > rect.top + rect.height - 120) continue;
    return { t: b[i][0], high: alto, i, x, y };
  }
  return null;
});
// La escala se reajusta sola con cada vela en vivo: se mide hasta que dos
// lecturas seguidas coinciden, o el clic saldría contra coordenadas viejas.
const diana = async () => {
  // Acercar primero: con el gráfico alejado una vela mide un píxel y varias
  // comparten máximo dentro del radio del imán, así que "la de al lado" sería
  // una respuesta igual de buena y el test no podría exigir una.
  await page.evaluate(() => {
    const b = window.__test.getBars().length;
    window.__test.chart.timeScale().setVisibleLogicalRange({ from: b - 120, to: b - 1 });
  });
  await wait(600);
  let a = await medirDiana();
  for (let i = 0; i < 6; i++) {
    await wait(250);
    const b = await medirDiana();
    if (a && b && b.t === a.t && Math.abs(b.x - a.x) < 1 && Math.abs(b.y - a.y) < 1) return b;
    a = b;
  }
  return a;
};
// ¿Sobre qué extremo de qué vela ha caído el punto? (exactamente, no "cerca")
const sobreExtremo = (pt) => page.evaluate((p) => {
  const b = window.__test.getBars();
  const i = b.findIndex(x => x[0] === p.t);
  if (i < 0) return { ok: false, motivo: 'el tiempo no es el de ninguna vela' };
  const vals = { apertura: b[i][1], máximo: b[i][2], mínimo: b[i][3], cierre: b[i][4] };
  const cual = Object.keys(vals).find(k => vals[k] === p.p);
  return { ok: !!cual, cual, i };
}, pt);

const d = await diana();
check('hay una vela con máximo local en zona despejada para probar el imán', !!d,
  d ? `vela ${d.i} en (${d.x.toFixed(0)}, ${d.y.toFixed(0)})` : 'ninguna');
await page.click('button[data-tool="__magnet"]');
await wait(200);
check('el botón del imán se enciende',
  await page.evaluate(() => window.__test.engine.iman === true
    && document.querySelector('button[data-tool="__magnet"]').classList.contains('active')));
await page.click('button[data-tool="hline"]');
await page.mouse.click(d.x + 1, d.y - 7);        // 7 px por encima del máximo
await wait(500);
const conIman = (await estado()).figuras[0].pts[0];
const enganche = await sobreExtremo(conIman);
check('con el imán, el punto cae EXACTAMENTE sobre el máximo de una vela',
  enganche.ok && enganche.cual === 'máximo' && Math.abs(enganche.i - d.i) <= 1,
  `clic 7 px sobre el máximo ${d.high} (vela ${d.i}) → ${conIman.p} en la ${enganche.i} (${enganche.cual || enganche.motivo})`);

await limpiar();
await page.keyboard.press('m');                   // atajo de teclado
await wait(200);
check('la tecla M apaga el imán',
  await page.evaluate(() => window.__test.engine.iman === false));
await page.click('button[data-tool="hline"]');
await page.mouse.click(d.x + 1, d.y - 7);
await wait(500);
const sinIman = (await estado()).figuras[0].pts[0];
const sinEnganche = await sobreExtremo(sinIman);
check('sin imán, el punto se queda donde se pinchó',
  !sinEnganche.ok, `${sinIman.p.toFixed(2)} (${sinEnganche.cual || sinEnganche.motivo})`);

// el imán se acuerda entre sesiones
await page.keyboard.press('m');
await wait(200);
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(700);
check('el imán se recuerda entre sesiones',
  await page.evaluate(() => window.__test.engine.iman === true
    && document.querySelector('button[data-tool="__magnet"]').classList.contains('active')));
await page.evaluate(() => window.__test.engine.setIman(false));
await limpiar();

// --------------------------------------------- F4-3.4 ocultar y bloquear
await limpiar();
await crear('rect', [[500, 300], [850, 480]]);
await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  s.style.color = '#ff00ff'; s.style.width = 3;
  s.style.fill = '#ff00ff'; s.style.fillOpacity = 0.5;
  e.redraw();
});
await wait(400);
await page.mouse.move(20, 780);            // el crosshair fuera del lienzo
await wait(300);
const magentaVisible = Object.entries(await pixeles())
  .filter(([k]) => { const [r, g, b] = k.split(',').map(Number); return r > 150 && b > 150 && g < 90; })
  .reduce((a, [, v]) => a + v, 0);
await page.keyboard.press('h');
await wait(500);
const pxOculto = await pixeles();
const magentaOculto = Object.entries(pxOculto)
  .filter(([k]) => { const [r, g, b] = k.split(',').map(Number); return r > 150 && b > 150 && g < 90; })
  .reduce((a, [, v]) => a + v, 0);
check('H oculta todos los dibujos', magentaVisible > 2000 && magentaOculto === 0,
  `${magentaVisible} → ${magentaOculto} px del dibujo`);
check('y las velas siguen ahí', cuenta(pxOculto, '112,146,190') + cuenta(pxOculto, '218,218,218') > 1000,
  `${cuenta(pxOculto, '112,146,190') + cuenta(pxOculto, '218,218,218')} px de vela`);
check('el dibujo sigue existiendo, solo no se ve', (await estado()).n === 1);
await page.keyboard.press('h');
await wait(500);
const magentaVuelto = Object.entries(await pixeles())
  .filter(([k]) => { const [r, g, b] = k.split(',').map(Number); return r > 150 && b > 150 && g < 90; })
  .reduce((a, [, v]) => a + v, 0);
check('y vuelve a verse al pulsar H otra vez', magentaVuelto > 2000, `${magentaVuelto} px`);

// candado: el dibujo no se mueve y el gesto se lo queda el gráfico
const centroBloq = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.keyboard.press('l');
await wait(400);
const antesCandado = await estado();
await arrastrar(centroBloq.x, centroBloq.y, centroBloq.x - 180, centroBloq.y + 60);
const trasCandado = await estado();
check('con el candado el dibujo no se mueve',
  Math.abs(trasCandado.figuras[0].pts[0].t - antesCandado.figuras[0].pts[0].t) < 2
  && Math.abs(trasCandado.figuras[0].pts[0].p - antesCandado.figuras[0].pts[0].p) < 0.01,
  `Δt=${trasCandado.figuras[0].pts[0].t - antesCandado.figuras[0].pts[0].t}`);
check('y el gesto lo aprovecha el gráfico para desplazarse',
  Math.abs(trasCandado.rango.from - (trasCandado.velas - antesCandado.velas) - antesCandado.rango.from) > 1,
  `${antesCandado.rango.from} → ${trasCandado.rango.from}`);
check('con el candado tampoco se selecciona', trasCandado.sel === null && !trasCandado.panel);
await page.keyboard.press('l');
await wait(400);
const centroLibre = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(centroLibre.x, centroLibre.y);
await wait(300);
const antesLibre = await estado();
await arrastrar(centroLibre.x, centroLibre.y, centroLibre.x + 120, centroLibre.y - 50);
const trasLibre = await estado();
check('al quitar el candado vuelve a moverse',
  trasLibre.figuras[0].pts[0].t > antesLibre.figuras[0].pts[0].t && noSeMovio(antesLibre, trasLibre),
  `Δt=${trasLibre.figuras[0].pts[0].t - antesLibre.figuras[0].pts[0].t}s`);

// los dos interruptores se recuerdan
await page.evaluate(() => { window.__test.engine.setOcultos(true); window.__test.engine.setBloqueados(true); });
await page.reload();
await page.waitForFunction(() => window.__test && window.__test.getBars().length > 100, { timeout: 30000 });
await wait(700);
check('ocultar y bloquear se recuerdan entre sesiones',
  await page.evaluate(() => window.__test.engine.ocultos === true && window.__test.engine.bloqueados === true
    && document.querySelector('button[data-tool="__hide"]').classList.contains('active')
    && document.querySelector('button[data-tool="__lock"]').classList.contains('active')));
// elegir herramienta con los dibujos ocultos los enseña: dibujar a ciegas, no
await page.click('button[data-tool="trend"]');
await wait(300);
check('elegir una herramienta deja de ocultar los dibujos',
  await page.evaluate(() => window.__test.engine.ocultos === false));
await page.keyboard.press('Escape');
await page.evaluate(() => { window.__test.engine.setOcultos(false); window.__test.engine.setBloqueados(false); });
await limpiar();


// ------------------------------------------------------- F4-3.5 duplicar
await limpiar();
await crear('trend', [[400, 300], [700, 400]]);
await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  s.style.color = '#ffcc00'; s.style.width = 3; e.redraw();
});
const gTrend2 = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.click(gTrend2.x, gTrend2.y);
await wait(300);
await page.keyboard.press('Control+c');
await page.keyboard.press('Control+v');
await wait(600);
const pegado = await estado();
check('Ctrl+C / Ctrl+V duplica el dibujo seleccionado', pegado.n === 2,
  `${pegado.n} figuras: ${pegado.tipos.join(', ')}`);
check('la copia conserva el estilo del original',
  pegado.n === 2 && pegado.figuras[1].style.color === '#ffcc00' && pegado.figuras[1].style.width === 3,
  JSON.stringify(pegado.figuras[1]?.style));
check('y aparece desplazada, no encima',
  pegado.n === 2 && pegado.figuras[1].pts[0].t > pegado.figuras[0].pts[0].t
  && pegado.figuras[1].pts[0].p < pegado.figuras[0].pts[0].p,
  `Δt=${pegado.figuras[1].pts[0].t - pegado.figuras[0].pts[0].t}s`);
check('la copia queda seleccionada', pegado.sel === pegado.figuras[1].id);
await page.keyboard.press('Control+z');
await wait(600);
check('deshacer se lleva la copia', (await estado()).n === 1);

// Alt + arrastrar: el original se queda y la copia se va con el ratón
const antesAlt = await estado();
const gAlt = await page.evaluate(() => {
  const e = window.__test.engine, s = e.shapes[0];
  const pts = e.screenPoints(s), r = e.paneRect();
  return { x: r.left + (pts[0].x + pts[1].x) / 2, y: r.top + (pts[0].y + pts[1].y) / 2 };
});
await page.mouse.move(gAlt.x, gAlt.y);
await page.keyboard.down('Alt');
await page.mouse.down();
for (let i = 1; i <= 10; i++) await page.mouse.move(gAlt.x + 14 * i, gAlt.y - 5 * i);
await page.mouse.up();
await page.keyboard.up('Alt');
await wait(500);
const trasAlt = await estado();
const original = trasAlt.figuras.find(f => f.id === antesAlt.figuras[0].id);
const copia = trasAlt.figuras.find(f => f.id !== antesAlt.figuras[0].id);
check('Alt + arrastrar duplica y mueve la copia',
  trasAlt.n === 2 && original && copia
  && Math.abs(original.pts[0].t - antesAlt.figuras[0].pts[0].t) < 2
  && copia.pts[0].t > original.pts[0].t && copia.pts[0].p > original.pts[0].p,
  `original quieto (Δt=${original ? original.pts[0].t - antesAlt.figuras[0].pts[0].t : '?'}), copia Δt=${copia ? copia.pts[0].t - original.pts[0].t : '?'}s`);
check('y el gráfico no se movió', noSeMovio(antesAlt, trasAlt),
  `${antesAlt.rango.from} → ${trasAlt.rango.from} (+${trasAlt.velas - antesAlt.velas} velas)`);
await limpiar();

// --------------------------------------------------- 9. limpieza final
await browser.close();
await restaurar();
console.log(fails.length ? `\nFALLOS: ${fails.join(', ')}` : '\nTODO OK');
process.exit(fails.length ? 1 : 0);
