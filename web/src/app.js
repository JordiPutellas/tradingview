// Frontend F1c: gráfico BTCUSDT perp con LWC v5.
// - 24+ timeframes (RF-5.3), lazy-loading por rango visible (RF-5.2)
// - streaming vía WS con series.update(), nunca setData en caliente (RF-5.1)
// - dibujos persistentes en precio+tiempo UTC absoluto (RF-4.3 / RF-5.5)
// - zona horaria de PRESENTACIÓN Europe/Madrid con DST; todo lo interno en UTC
import { createChart, CandlestickSeries } from 'lightweight-charts';
import {
  DrawingManager, HorizontalLine, HorizontalRay, TrendLine, Rectangle,
  Curve, Arc, Circle, TextAnnotation,
} from 'lightweight-charts-drawing';

const $ = (sel) => document.querySelector(sel);
const statusEl = $('#status');

// ---------- configuración ----------
// Ajustable en caliente sin recompilar, desde la consola del navegador:
//   localStorage.setItem('cfg.wheelZoom', '0.45'); location.reload()
const cfgNum = (k, def) => {
  const v = parseFloat(localStorage.getItem(`cfg.${k}`));
  return Number.isFinite(v) ? v : def;
};
const CONFIG = {
  // Fracción del rango visible que se come cada muesca de rueda. LWC nativo
  // mueve el barSpacing un 10% por muesca: hay que girar muchísimo.
  wheelZoom: cfgNum('wheelZoom', 0.18),
  // Márgenes de la escala de precio (fracción del alto). El defecto de LWC es
  // top 0.2: demasiado aire sobre el precio.
  priceMarginTop: cfgNum('priceMarginTop', 0.06),
  priceMarginBottom: cfgNum('priceMarginBottom', 0.16),
  // 0 = el autoajuste mira solo las velas; 1 = también los dibujos.
  drawingsAutoscale: cfgNum('drawingsAutoscale', 0) === 1,
};

// Helper de consola para no pelearse con localStorage a mano:
//   cfg.list()                 qué hay en vigor
//   cfg.set('wheelZoom', 0.25) ajusta y recarga
//   cfg.reset()                vuelve a los valores por defecto
window.cfg = {
  list: () => ({ ...CONFIG }),
  set(k, v) { localStorage.setItem(`cfg.${k}`, String(v)); location.reload(); },
  reset(k) {
    if (k) localStorage.removeItem(`cfg.${k}`);
    else Object.keys(localStorage).filter(x => x.startsWith('cfg.')).forEach(x => localStorage.removeItem(x));
    location.reload();
  },
};

// Paleta de las velas: cuerpo, borde y mecha del mismo color (sin outline).
const UP = '#7092be', DOWN = '#dadada';
// Crosshair: sólido, oscuro y de 1 px (el defecto de LWC es discontinuo y claro).
const CROSSHAIR = '#1e1e1e';

// ---------- timeframes ----------
// Anclajes idénticos a los de la API: semanas/3D/5D sobre 2018-01-01 (lunes),
// meses de calendario UTC. bucketStart() debe cuadrar con time_bucket().
const WEEK_ORIGIN = Date.UTC(2018, 0, 1) / 1000;
const TFS = [
  ['1s',1],['5s',5],['10s',10],['15s',15],['30s',30],['45s',45],
  ['1m',60],['3m',180],['5m',300],['15m',900],['30m',1800],['45m',2700],
  ['1h',3600],['2h',7200],['3h',10800],['4h',14400],['6h',21600],['8h',28800],['12h',43200],
  ['1D',86400],['3D',259200],['5D',432000],['1W',604800],['2W',1209600],
  ['1M',0],['3M',0],['6M',0],['12M',0], // 0 = bucket de calendario
].map(([name, seconds]) => ({ name, seconds }));

function bucketStart(tf, t) {
  if (tf.seconds > 0) {
    if (['3D','5D','1W','2W'].includes(tf.name)) {
      return t - ((t - WEEK_ORIGIN) % tf.seconds);
    }
    return t - (t % tf.seconds);
  }
  const months = { '1M':1, '3M':3, '6M':6, '12M':12 }[tf.name];
  const d = new Date(t * 1000);
  const m = d.getUTCFullYear() * 12 + d.getUTCMonth();
  const anchored = m - (m % months);
  return Date.UTC(Math.floor(anchored / 12), anchored % 12, 1) / 1000;
}

// ---------- gráfico ----------
const madrid = new Intl.DateTimeFormat('es-ES', {
  timeZone: 'Europe/Madrid', hour: '2-digit', minute: '2-digit', second: '2-digit',
  day: '2-digit', month: '2-digit', year: 'numeric', hour12: false,
});
function fmtParts(t) {
  const p = Object.fromEntries(madrid.formatToParts(new Date(t * 1000)).map(x => [x.type, x.value]));
  return p;
}
function fmtFull(t) {
  const p = fmtParts(t);
  return `${p.day}/${p.month}/${p.year} ${p.hour}:${p.minute}:${p.second}`;
}
function fmtTick(t, tickType) {
  const p = fmtParts(t);
  // tickType (TickMarkType): 0 año, 1 mes, 2 día, 3 hora:min, 4 con segundos
  if (tickType === 0) return p.year;
  if (tickType === 1) return `${p.month}/${p.year}`;
  if (tickType === 2) return `${p.day}/${p.month}`;
  if (tickType === 3) return `${p.hour}:${p.minute}`;
  return `${p.hour}:${p.minute}:${p.second}`;
}

const container = $('#chart');
const chart = createChart(container, {
  autoSize: true,
  layout: {
    attributionLogo: true, // obligación de la licencia Apache 2.0 (RF-5.6)
    background: { color: '#363636' }, textColor: '#dadada',
  },
  grid: { vertLines: { visible: false }, horzLines: { visible: false } },
  timeScale: {
    timeVisible: true, secondsVisible: false, borderColor: '#4a4a4a',
    // Con el 0,5 por defecto solo caben ~2.600 velas en pantalla: al pedir
    // más, LWC recortaba el ancho de la ventana pero respetaba su posición y
    // el gráfico se quedaba fuera de la vista. Con 0,05 caben ~26.000.
    minBarSpacing: 0.05,
    tickMarkFormatter: (t, type) => fmtTick(t, type),
  },
  localization: { timeFormatter: (t) => fmtFull(t) },
  rightPriceScale: {
    borderColor: '#4a4a4a',
    scaleMargins: { top: CONFIG.priceMarginTop, bottom: CONFIG.priceMarginBottom },
  },
  crosshair: {
    mode: 0, // libre, no engancha a la vela
    vertLine: { color: CROSSHAIR, width: 1, style: 0, labelBackgroundColor: CROSSHAIR },
    horzLine: { color: CROSSHAIR, width: 1, style: 0, labelBackgroundColor: CROSSHAIR },
  },
  // El zoom de rueda lo hacemos nosotros (más abajo): el nativo es muy corto.
  handleScale: { mouseWheel: false },
});
const series = chart.addSeries(CandlestickSeries, {
  upColor: UP, downColor: DOWN, borderVisible: true,
  borderUpColor: UP, borderDownColor: DOWN, wickUpColor: UP, wickDownColor: DOWN,
});

// Zoom de rueda propio: escala el rango lógico visible alrededor del cursor.
// LWC solo ofrece handleScale.mouseWheel on/off (10% de barSpacing por
// muesca), sin sensibilidad; con esto una muesca se come CONFIG.wheelZoom.
container.addEventListener('wheel', (e) => {
  if (e.ctrlKey) return;                                    // zoom del navegador
  if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;      // desplazamiento lateral: lo lleva LWC
  const ts = chart.timeScale();
  const range = ts.getVisibleLogicalRange();
  if (!range) return;
  e.preventDefault();
  const notches = Math.max(-3, Math.min(3, e.deltaMode === 1 ? e.deltaY / 3 : e.deltaY / 100));
  const span = range.to - range.from;
  const width = ts.width() || container.clientWidth;
  const frac = Math.min(1, Math.max(0, (e.clientX - container.getBoundingClientRect().left) / width));
  const anchor = range.from + frac * span;

  // Topes: al fijar el rango lógico a mano nos saltamos los límites que LWC
  // aplica en su propio zoom, y alejando sin freno el gráfico acababa
  // empujado fuera de la pantalla — solo fondo. El tope de alejamiento es
  // "todo lo cargado" (que crece solo al desplazarse al pasado) y la ventana
  // se recoloca para que SIEMPRE queden velas a la vista.
  const n = bars.length || 1;
  const gap = Math.min(60, Math.max(3, n * 0.05));  // margen a los lados, en velas
  const maxSpan = n + 2 * gap;                      // alejarse más no enseña nada nuevo
  const newSpan = Math.max(6, Math.min(span * Math.pow(1 + CONFIG.wheelZoom, notches), maxSpan));
  let from = anchor - frac * newSpan, to = from + newSpan;
  // Con newSpan <= n + 2*gap, el primer recorte ya deja from >= -gap: los dos
  // topes no pueden pelearse (si lo hacían, el hueco se iba entero a un lado).
  if (to > n + gap) { to = n + gap; from = to - newSpan; }
  if (from < -gap) { from = -gap; to = from + newSpan; }
  ts.setVisibleLogicalRange({ from, to });
}, { passive: false });
const toCandle = ([t, o, h, l, c]) => ({ time: t, open: o, high: h, low: l, close: c });

// ---------- carga y lazy-loading ----------
let tf = TFS.find(x => x.name === '1m');
let bars = [];        // filas crudas [t,o,h,l,c,v] ascendentes
let loadingOlder = false, noMoreHistory = false;

async function fetchCandles(params) {
  const qs = new URLSearchParams({ tf: tf.name, ...params });
  const r = await fetch(`/api/candles?${qs}`);
  if (!r.ok) throw new Error(`api ${r.status}`);
  return r.json();
}

function render() {
  series.setData(bars.map(toCandle));
}

async function loadTF(next) {
  tf = next;
  bars = []; noMoreHistory = false; resetStream();
  document.querySelectorAll('.tfs button').forEach(b => b.classList.toggle('active', b.dataset.tf === tf.name));
  statusEl.textContent = `cargando ${tf.name}…`;
  bars = await fetchCandles({ limit: 1500 });
  render();
  chart.timeScale().resetTimeScale();
  chart.timeScale().scrollToRealTime();
  statusEl.textContent = tf.name;
}

chart.timeScale().subscribeVisibleLogicalRangeChange(async (range) => {
  if (!range || loadingOlder || noMoreHistory || !bars.length || range.from > 150) return;
  loadingOlder = true;
  try {
    const page = await fetchCandles({ to: bars[0][0], limit: 5000 });
    if (!page.length) { noMoreHistory = true; return; }
    const prev = chart.timeScale().getVisibleLogicalRange();
    bars = page.concat(bars);
    render(); // prepend histórico: setData aquí es el patrón correcto; el streaming usa update()
    chart.timeScale().setVisibleLogicalRange({ from: prev.from + page.length, to: prev.to + page.length });
  } finally { loadingOlder = false; }
});

// ---------- streaming (WS → agregación cliente al TF activo) ----------
// El servidor emite la vela de 1s en curso (~3/s). El cliente la funde en el
// bucket del TF activo; al cerrarse un bucket se re-pide a REST para dejarlo
// exacto (los buckets viejos ya vienen exactos del servidor).
let cur = null; // { t, o, h, l, c, vBase, lastSecT, lastSecV }
function resetStream() { cur = null; }

async function onTick(m) {
  if (!bars.length) return;
  const bt = bucketStart(tf, m.t);
  const last = bars[bars.length - 1];
  if (cur === null || bt > cur.t) {
    if (cur !== null && bt > cur.t) {
      // bucket cerrado: repóngase exacto desde la API (asíncrono, best-effort)
      fetchCandles({ from: cur.t, limit: 2 }).then(rows => {
        for (const row of rows) {
          const i = bars.findIndex(b => b[0] === row[0]);
          if (i >= 0) bars[i] = row;
          series.update(toCandle(row));
        }
      }).catch(() => {});
    }
    const carry = (last && last[0] === bt) ? last : null;
    cur = carry
      ? { t: bt, o: carry[1], h: carry[2], l: carry[3], c: m.c, vBase: carry[5], lastSecT: -1, lastSecV: 0 }
      : { t: bt, o: m.o, h: m.h, l: m.l, c: m.c, vBase: 0, lastSecT: -1, lastSecV: 0 };
  }
  if (m.t === cur.lastSecT) {
    cur.lastSecV = m.v; // misma vela de 1s actualizada: sustituye, no acumula
  } else {
    cur.vBase += cur.lastSecV;
    cur.lastSecT = m.t; cur.lastSecV = m.v;
  }
  cur.h = Math.max(cur.h, m.h); cur.l = Math.min(cur.l, m.l); cur.c = m.c;
  const bar = [cur.t, cur.o, cur.h, cur.l, cur.c, cur.vBase + cur.lastSecV];
  if (last && last[0] === cur.t) bars[bars.length - 1] = bar; else bars.push(bar);
  series.update(toCandle(bar));
  statusEl.textContent = `${tf.name} · ${m.c.toFixed(1)}`;
}

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/api/ws`);
  ws.onmessage = (ev) => { try { onTick(JSON.parse(ev.data)); } catch {} };
  ws.onclose = () => { statusEl.textContent = 'reconectando…'; setTimeout(connectWS, 2000); };
}

// ---------- dibujos ----------
const dm = new DrawingManager();
dm.attach(chart, series, container);

// El autoajuste de la escala debe mirar SOLO las velas: un rectángulo en 100k
// no puede forzar un zoom out al pulsar "auto". Cada dibujo del plugin es a la
// vez la primitive que LWC consulta y su clase base devuelve el rango de sus
// anclas en autoscaleInfo(); se anula por instancia al añadirlo.
function addDrawing(d) {
  if (!CONFIG.drawingsAutoscale) d.autoscaleInfo = () => null;
  dm.addDrawing(d);
  return d;
}

const zoneRegistry = new Map(); // lineId -> {zoneId, role:'a'|'b', siblingId}
const zones = new Map();        // zoneId -> {time, a:{price,...}, b:{price,...}}
let shiftDown = false;
addEventListener('keydown', e => { if (e.key === 'Shift') shiftDown = true; });
addEventListener('keyup', e => { if (e.key === 'Shift') shiftDown = false; });

const uuid = () => crypto.randomUUID();

async function persistDrawing(id, payload) {
  await fetch(`/api/drawings/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
  }).catch(() => {});
}
async function deleteDrawing(id) {
  await fetch(`/api/drawings/${id}`, { method: 'DELETE' }).catch(() => {});
}

const persistTimers = new Map();
function schedulePersist(drawing) {
  const reg = zoneRegistry.get(drawing.id);
  const key = reg ? reg.zoneId : drawing.id;
  clearTimeout(persistTimers.get(key));
  persistTimers.set(key, setTimeout(() => {
    if (reg) persistDrawing(key, { kind: 'zone2', data: zones.get(key) });
    else persistDrawing(key, { kind: 'plugin', data: drawing.toJSON() });
  }, 400));
}

// Zona de dos niveles: dos HorizontalRay agrupados. Arrastre normal = las dos
// juntas manteniendo distancia; con Shift = solo la agarrada (ajuste de
// separación). El handle izquierdo es solo punto de agarre.
function zoneLine(zoneId, role, time, spec) {
  const id = `${zoneId}:${role}`;
  // Construcción SIEMPRE con anchors explícitos: el `create` estático
  // heredado tiene otro orden de argumentos (id, time, price) y colocaba el
  // precio en el tiempo. Con el objeto anchor no hay ambigüedad.
  const line = new HorizontalRay(id, [{ time, price: spec.price }],
    { color: spec.color, lineWidth: spec.lineWidth }, { direction: 'right', showPrice: true });
  addDrawing(line);
  zoneRegistry.set(id, { zoneId, role, siblingId: `${zoneId}:${role === 'a' ? 'b' : 'a'}` });
  return line;
}

function createZone(zoneId, spec) {
  zones.set(zoneId, spec);
  zoneLine(zoneId, 'a', spec.time, spec.a);
  zoneLine(zoneId, 'b', spec.time, spec.b);
}

let syncingZone = false;
dm.on('drawing:updated', ({ drawing }) => {
  if (!drawing) return;
  const reg = zoneRegistry.get(drawing.id);
  if (reg && !syncingZone) {
    const zone = zones.get(reg.zoneId);
    const role = reg.role;
    const newPrice = drawing.anchors[0].price;
    const delta = newPrice - zone[role].price;
    zone[role].price = newPrice;
    zone.time = drawing.anchors[0].time;
    if (!shiftDown && delta !== 0) {
      const sib = dm.getDrawing(reg.siblingId);
      if (sib) {
        syncingZone = true;
        const other = role === 'a' ? 'b' : 'a';
        zone[other].price += delta;
        sib.updateAnchor(0, { time: zone.time, price: zone[other].price });
        syncingZone = false;
      }
    } else {
      const sib = dm.getDrawing(reg.siblingId);
      if (sib) { syncingZone = true; sib.updateAnchor(0, { time: zone.time, price: zone[reg.role === 'a' ? 'b' : 'a'].price }); syncingZone = false; }
    }
  }
  schedulePersist(drawing);
});

// ---------- creación por clicks ----------
const TOOL_DEFS = {
  hline: { clicks: 1, make: (id, pts) => HorizontalLine.create(id, pts[0].price, pts[0].time, style(), { extendLeft: true, extendRight: true }) },
  hray:  { clicks: 1, make: (id, pts) => new HorizontalRay(id, [pts[0]], style(), { direction: 'right' }) },
  trend: { clicks: 2, make: (id, pts) => new TrendLine(id, pts, style()) },
  rect:  { clicks: 2, make: (id, pts) => new Rectangle(id, pts, { ...style(), fillColor: 'rgba(240,185,11,.12)' }) },
  curve: { clicks: 4, make: (id, pts) => new Curve(id, pts, style()) },
  arc:   { clicks: 3, make: (id, pts) => new Arc(id, pts, style()) },
  point: { clicks: 1, make: (id, pts) => {
    // "Punto": círculo pequeño relleno; el segundo ancla (radio) se deriva
    // del rango de precio visible para que se vea como un punto.
    const p = pts[0];
    const r = { time: p.time, price: p.price * 1.0008 };
    return new Circle(id, [p, r], style(), { filled: true });
  } },
  text:  { clicks: 1, make: (id, pts) => {
    const t = prompt('Texto:', ''); if (!t) return null;
    return new TextAnnotation(id, pts, style(), { text: t });
  } },
};
function style() { return { color: '#f0b90b', lineWidth: 1 }; }

let activeTool = null, pendingPoints = [];
function setTool(name) {
  activeTool = name; pendingPoints = [];
  dm.setActiveTool(name); // bloquea la selección del plugin mientras se crea
  document.querySelectorAll('.tools button').forEach(b => b.classList.toggle('active', b.dataset.tool === name));
}

chart.subscribeClick((param) => {
  if (!activeTool || !param.point || param.time === undefined) return;
  const price = series.coordinateToPrice(param.point.y);
  if (price === null) return;
  pendingPoints.push({ time: param.time, price });

  if (activeTool === 'zone2') {
    if (pendingPoints.length < 2) return;
    const id = uuid();
    const [p1, p2] = pendingPoints;
    const spec = {
      time: p1.time,
      a: { price: p1.price, color: '#f0b90b', lineWidth: 2 },
      b: { price: p2.price, color: '#3fb950', lineWidth: 1 },
    };
    createZone(id, spec);
    persistDrawing(id, { kind: 'zone2', data: spec });
    setTool(null);
    return;
  }
  const def = TOOL_DEFS[activeTool];
  if (!def || pendingPoints.length < def.clicks) return;
  const id = uuid();
  const d = def.make(id, pendingPoints);
  setTool(null);
  if (!d) return;
  addDrawing(d);
  dm.selectDrawing(id);
  persistDrawing(id, { kind: 'plugin', data: d.toJSON() });
});

addEventListener('keydown', (e) => {
  if (e.key === 'Escape') setTool(null);
  if ((e.key === 'Delete' || e.key === 'Backspace') && document.activeElement === document.body) {
    const sel = dm.getSelectedDrawing();
    if (!sel) return;
    const reg = zoneRegistry.get(sel.id);
    if (reg) {
      const zone = zones.get(reg.zoneId);
      dm.removeDrawing(`${reg.zoneId}:a`); dm.removeDrawing(`${reg.zoneId}:b`);
      zoneRegistry.delete(`${reg.zoneId}:a`); zoneRegistry.delete(`${reg.zoneId}:b`);
      zones.delete(reg.zoneId);
      deleteDrawing(reg.zoneId);
    } else {
      dm.removeDrawing(sel.id);
      deleteDrawing(sel.id);
    }
  }
});

// factory para reimportar dibujos del plugin desde su JSON
const CLASSES = { 'horizontal-line': HorizontalLine, 'horizontal-ray': HorizontalRay,
  'trend-line': TrendLine, rectangle: Rectangle, curve: Curve, arc: Arc,
  circle: Circle, 'text-annotation': TextAnnotation };
function restorePlugin(data) {
  const cls = CLASSES[data.type];
  if (!cls) return;
  const d = new cls(data.id);
  d.fromJSON(data);
  addDrawing(d);
}

async function loadDrawings() {
  const rows = await fetch('/api/drawings').then(r => r.json()).catch(() => []);
  for (const row of rows) {
    if (row.payload.kind === 'zone2') createZone(row.id, row.payload.data);
    else if (row.payload.kind === 'plugin') restorePlugin({ ...row.payload.data, id: row.id });
  }
}

// ---------- toolbar ----------
const tfsEl = $('#tfs');
for (const t of TFS) {
  const b = document.createElement('button');
  b.textContent = t.name; b.dataset.tf = t.name;
  b.onclick = () => loadTF(t);
  tfsEl.appendChild(b);
}
document.querySelectorAll('.tools button').forEach(b => {
  b.onclick = () => {
    if (b.dataset.tool === '__clear') {
      dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete' }));
      return;
    }
    setTool(activeTool === b.dataset.tool ? null : b.dataset.tool);
  };
});

// La barra de timeframes es de UNA línea: lo que no cabe se desplaza con la
// rueda o arrastrando. El arrastre no debe disparar el click del botón.
let tfDrag = null, tfDragged = false;
tfsEl.addEventListener('wheel', (e) => {
  e.preventDefault();
  tfsEl.scrollLeft += Math.abs(e.deltaY) > Math.abs(e.deltaX) ? e.deltaY : e.deltaX;
}, { passive: false });
tfsEl.addEventListener('pointerdown', (e) => {
  if (e.button !== 0) return;
  tfDrag = { x: e.clientX, left: tfsEl.scrollLeft };
  tfDragged = false;
  addEventListener('pointermove', onTfMove);
  addEventListener('pointerup', onTfUp);
});
function onTfMove(e) {
  const dx = e.clientX - tfDrag.x;
  if (!tfDragged && Math.abs(dx) > 3) { tfDragged = true; tfsEl.classList.add('dragging'); }
  if (tfDragged) tfsEl.scrollLeft = tfDrag.left - dx;
}
function onTfUp() {
  removeEventListener('pointermove', onTfMove);
  removeEventListener('pointerup', onTfUp);
  tfDrag = null; tfsEl.classList.remove('dragging');
}
tfsEl.addEventListener('click', (e) => {
  if (tfDragged) { e.stopPropagation(); e.preventDefault(); tfDragged = false; }
}, true);

// ---------- barra de dibujo flotante ----------
// Arrastrable por el asa y con la posición guardada entre sesiones.
const toolsEl = $('#tools'), gripEl = $('#toolsGrip');
const TOOLBAR_KEY = 'btcdash.toolbarPos';
function placeToolbar(x, y) {
  const r = toolsEl.getBoundingClientRect();
  const pos = {
    x: Math.max(0, Math.min(x, innerWidth - r.width)),
    y: Math.max(0, Math.min(y, innerHeight - r.height)),
  };
  toolsEl.style.left = `${pos.x}px`;
  toolsEl.style.top = `${pos.y}px`;
  return pos;
}
try {
  const saved = JSON.parse(localStorage.getItem(TOOLBAR_KEY) || 'null');
  if (saved) placeToolbar(saved.x, saved.y);
} catch { /* posición corrupta: se queda la de por defecto */ }

gripEl.addEventListener('pointerdown', (e) => {
  if (e.button !== 0) return;
  e.preventDefault();
  const r = toolsEl.getBoundingClientRect();
  const dx = e.clientX - r.left, dy = e.clientY - r.top;
  toolsEl.classList.add('dragging');
  const move = (ev) => placeToolbar(ev.clientX - dx, ev.clientY - dy);
  const up = (ev) => {
    removeEventListener('pointermove', move);
    removeEventListener('pointerup', up);
    toolsEl.classList.remove('dragging');
    localStorage.setItem(TOOLBAR_KEY, JSON.stringify(placeToolbar(ev.clientX - dx, ev.clientY - dy)));
  };
  addEventListener('pointermove', move);
  addEventListener('pointerup', up);
});
addEventListener('resize', () => {
  const r = toolsEl.getBoundingClientRect();
  placeToolbar(r.left, r.top); // que no se quede fuera al encoger la ventana
});

// ---------- arranque ----------
(async () => {
  await loadTF(tf);
  await loadDrawings();
  connectWS();
})();

// hooks de test (DST, streaming) — sin efecto en producción
window.__test = { bucketStart, fmtTick, fmtFull, TFS, dm, loadTF, chart, series,
  getBars: () => bars, getTF: () => tf, setTool, zones, CONFIG, tfsEl, toolsEl,
  TOOL_DEFS, addDrawing };
