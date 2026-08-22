// Frontend F1c: gráfico BTCUSDT perp con LWC v5.
// - 24+ timeframes (RF-5.3), lazy-loading por rango visible (RF-5.2)
// - streaming vía WS con series.update(), nunca setData en caliente (RF-5.1)
// - dibujos persistentes en precio+tiempo UTC absoluto (RF-4.3 / RF-5.5)
// - zona horaria de PRESENTACIÓN Europe/Madrid con DST; todo lo interno en UTC
import { createChart, CandlestickSeries } from 'lightweight-charts';
import { DrawEngine } from './draw/engine.js';
import { mountPanel } from './draw/panel.js';
import { logicalOf as aLogico, timeOfLogical as aTiempo } from './timemap.js';
import { mountLegend } from './legend.js';

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
  // Radio del imán en píxeles (F4-3.3): a cuánto tiene que estar el cursor
  // del máximo/mínimo/apertura/cierre de la vela para engancharse.
  magnetPx: cfgNum('magnetPx', 12),
  // Tope de velas VISIBLES al conservar el rango cambiando de timeframe
  // (F4-1.1). Dos años en 1h son ~17.500 velas; en 1s serían 60 millones.
  // Pasado el tope se centra en el mismo instante y se enseñan las que caben.
  tfChangeMaxBars: cfgNum('tfChangeMaxBars', 12000),
  // Y el suelo: pasar de 1s a 1h conservando cuatro minutos deja UNA vela en
  // pantalla. Por debajo de este número se ensancha alrededor del mismo
  // instante, que es lo que hace útil el cambio.
  tfChangeMinBars: cfgNum('tfChangeMinBars', 20),
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

// Segundos por vela de un timeframe cualquiera. Los de calendario declaran
// seconds=0 (su bucket real varía), así que se usa la misma aproximación que
// la API para el tamaño nominal.
const MESES_SEG = { '1M': 30 * 86400, '3M': 90 * 86400, '6M': 180 * 86400, '12M': 365 * 86400 };
const pasoDe = (t) => (t.seconds > 0 ? t.seconds : MESES_SEG[t.name] || 30 * 86400);

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
const MAX_FETCH = 20000;             // tope por petición de la API (RF-4.1)
const ahora = () => Math.floor(Date.now() / 1000);

let tf = TFS.find(x => x.name === '1m');
let bars = [];        // filas crudas [t,o,h,l,c,v] ascendentes
let loadingOlder = false, loadingNewer = false, noMoreHistory = false;
// liveEdge: las velas cargadas llegan hasta el presente. Al restaurar un rango
// del pasado NO llegan, y entonces el streaming no puede tocar `bars`: metería
// la vela de ahora justo detrás de una de 2020.
let liveEdge = true;
// montando: hay un cambio de timeframe a medias. Entre el setData y el
// setVisibleLogicalRange, LWC emite el rango VIEJO medido sobre las velas
// NUEVAS, y eso disparaba una carga de histórico que no venía a cuento (5.000
// velas de más y un salto de la vista).
let montando = false;
// Última pulsación manda: con las flechas (F4-1.2) es fácil encadenar cambios
// de timeframe más rápido de lo que responde la API.
let seq = 0;

// Las peticiones de una carga que ya no interesa se abortan: encadenar
// timeframes con las flechas dejaba consultas caras (1s, 12M) corriendo contra
// la base de datos para tirar el resultado a la basura.
let enVuelo = null;
async function fetchCandles(params, señal) {
  const qs = new URLSearchParams({ tf: tf.name, ...params });
  const r = await fetch(`/api/candles?${qs}`, señal ? { signal: señal } : undefined);
  if (!r.ok) throw new Error(`api ${r.status}`);
  return r.json();
}

// La legend se monta más abajo (necesita `bars`), pero render() ya la busca:
// declarada aquí para no depender del orden de evaluación.
let legend = null;
function render() {
  series.setData(bars.map(toCandle));
  if (legend) legend.refresh();
}

// Rango visible en TIEMPO absoluto (no en índices): es lo único que sobrevive
// a un cambio de timeframe, donde los índices significan otra cosa.
function visibleTimeRange() {
  const r = chart.timeScale().getVisibleLogicalRange();
  if (!r || !bars.length) return null;
  const step = barStep();
  const from = aTiempo(bars, step, r.from), to = aTiempo(bars, step, r.to);
  return Number.isFinite(from) && Number.isFinite(to) && to > from ? { from, to } : null;
}

function setVisibleTimeRange(from, to) {
  const step = barStep();
  const l1 = aLogico(bars, step, from), l2 = aLogico(bars, step, to);
  if (l1 === null || l2 === null || !(l2 > l1)) return false;
  chart.timeScale().setVisibleLogicalRange({ from: l1, to: l2 });
  return true;
}

// Carga el tramo [from, to] en el timeframe activo y lo deja a la vista.
// Devuelve false si ese tramo no existe en este timeframe (p. ej. 2020 en 1s,
// que empieza en 2024): el llamante decide qué hacer.
async function cargarRango(vista, mine, señal) {
  const step = barStep();
  const cap = Math.max(50, CONFIG.tfChangeMaxBars);
  let { from, to } = vista;
  let velas = (to - from) / step, aviso = '';
  const centro = (from + to) / 2;
  if (velas > cap) {
    // No cabe: mismo instante en el centro y tantas velas como permite el tope.
    from = centro - (cap * step) / 2;
    to = centro + (cap * step) / 2;
    velas = cap; aviso = `${cap} velas (tope)`;
  } else if (velas < CONFIG.tfChangeMinBars) {
    velas = CONFIG.tfChangeMinBars;
    from = centro - (velas * step) / 2;
    to = centro + (velas * step) / 2;
  }
  // Margen a los lados para poder desplazarse un poco sin volver al servidor,
  // sin pasarse del tope de la API: si se pasara, devolvería solo las primeras
  // MAX_FETCH velas (ASC) y el lado derecho de la vista quedaría vacío. El
  // suelo de 200 velas evita que el propio umbral del lazy-loading (150) se
  // dispare nada más pintar y encadene páginas de 5.000 sin necesidad.
  const margen = Math.min(Math.max(0.25 * velas, 200), Math.max(0, (MAX_FETCH - velas) / 2)) * step;
  const filas = await fetchCandles({
    // alineado al bucket para que la primera vela no salga cortada en los
    // timeframes que se agregan al vuelo (45m, 3h, 3D, semanas, meses)
    from: bucketStart(tf, Math.floor(from - margen)),
    to: Math.ceil(to + margen),
    limit: MAX_FETCH,
  }, señal);
  if (mine !== seq) return true;          // llegó tarde: manda otra carga
  if (filas.length < 2) return false;     // ese tramo no existe en este timeframe
  bars = filas;
  render();
  liveEdge = bars[bars.length - 1][0] >= ahora() - 2 * step;
  setVisibleTimeRange(from, to);
  avisar(aviso);
  return true;
}

// loadTF(next, opts):
//   opts.view  {from,to} rango a restaurar; null = ir al presente;
//              sin definir = conservar el que se está viendo (F4-1.1)
//   opts.span  nº de velas a la vista al ir al presente (F4-1.3)
// Modo de las flechas (F4-1.2b): en vez de conservar el rango temporal se
// conserva el ANCHO DE VELA, o sea el número de velas a la vista. Bajar de
// temporalidad acerca y subir aleja, que es lo que se espera de un cambio
// hecho con el teclado mientras se mira un tramo.
//
// Qué se queda quieto: pegados al presente, el borde derecho —se sigue viendo
// la última vela—; mirando el pasado, el centro de la pantalla, que es lo que
// se está observando.
function mismasVelas(next) {
  const r = chart.timeScale().getVisibleLogicalRange();
  const v = visibleTimeRange();
  if (!r || !v) return {};                       // sin datos aún: carga normal
  const velas = Math.max(CONFIG.tfChangeMinBars,
    Math.min(CONFIG.tfChangeMaxBars, r.to - r.from));
  if (liveEdge && r.to >= bars.length - 1) return { view: null, span: velas };
  const centro = (v.from + v.to) / 2, mitad = (velas * pasoDe(next)) / 2;
  return { view: { from: centro - mitad, to: centro + mitad } };
}

async function loadTF(next, opts = {}) {
  if (opts.mismasVelas) opts = { ...opts, ...mismasVelas(next) };
  const vista = opts.view !== undefined ? opts.view : visibleTimeRange();
  const mine = ++seq;
  montando = true;
  if (enVuelo) enVuelo.abort();
  const ctrl = enVuelo = new AbortController();
  try {
    await cargarTF(next, opts, vista, mine, ctrl.signal);
  } catch (e) {
    if (e.name !== 'AbortError') throw e;      // la carga la ha sustituido otra
  } finally {
    if (mine === seq) { montando = false; enVuelo = null; }
  }
}

async function cargarTF(next, opts, vista, mine, señal) {
  tf = next;
  bars = []; noMoreHistory = false; liveEdge = true; resetStream();
  document.querySelectorAll('.tfs button').forEach(b => b.classList.toggle('active', b.dataset.tf === tf.name));
  statusEl.textContent = `cargando ${tf.name}…`;
  if (vista && await cargarRango(vista, mine, señal)) { saveView(); return; }
  if (mine !== seq) return;
  const span = Math.min(opts.span || 0, CONFIG.tfChangeMaxBars);
  const filas = await fetchCandles({ limit: Math.max(1500, Math.round(span) + 500) }, señal);
  if (mine !== seq) return;
  bars = filas;
  render();
  chart.timeScale().resetTimeScale();
  chart.timeScale().scrollToRealTime();
  if (span > 1) {
    // Contado desde la ÚLTIMA vela y no desde el rango que devuelva LWC en
    // este instante: scrollToRealTime() no ha terminado necesariamente de
    // aplicarse y leerlo aquí dejaba la ventana semanas por detrás del
    // presente (lo pilló el test del borde derecho con las flechas).
    const n = bars.length;
    chart.timeScale().setVisibleLogicalRange({ from: n - span, to: n });
  }
  avisar(vista ? 'sin datos en ese tramo' : '');
  saveView();
}

// El streaming reescribe el status tres veces por segundo, así que un aviso
// puesto ahí a pelo duraba lo que un suspiro. Se guarda y lo repinta onTick.
let nota = '', notaHasta = 0;
function avisar(txt) {
  nota = txt;
  notaHasta = txt ? Date.now() + 8000 : 0;
  statusEl.textContent = estadoTexto();
}
function estadoTexto(precio) {
  if (nota && Date.now() > notaHasta) nota = '';
  return [tf.name, liveEdge ? '' : 'pasado · End vuelve', nota, precio].filter(Boolean).join(' · ');
}

// ---------- estado del gráfico entre sesiones (F4-1.3) ----------
const VIEW_KEY = 'btcdash.view';
let viewTimer = null;
function saveView() {
  clearTimeout(viewTimer);
  viewTimer = setTimeout(() => {
    const v = visibleTimeRange();
    const r = chart.timeScale().getVisibleLogicalRange();
    if (!v || !r) return;
    // "live" = el borde derecho está pegado a la última vela. Se guarda como
    // tal (y no como instante) porque al volver mañana el presente es otro.
    localStorage.setItem(VIEW_KEY, JSON.stringify({
      tf: tf.name, from: v.from, to: v.to,
      live: liveEdge && r.to >= bars.length - 1,
      span: r.to - r.from,
    }));
  }, 600);
}

function vistaGuardada() {
  try {
    const v = JSON.parse(localStorage.getItem(VIEW_KEY) || 'null');
    if (!v || !TFS.some(t => t.name === v.tf)) return null;
    return v;
  } catch { return null; }
}

chart.timeScale().subscribeVisibleLogicalRangeChange((range) => {
  if (montando) return;
  saveView();
  if (!range || !bars.length) return;
  if (!loadingOlder && !noMoreHistory && range.from <= 150) cargarMasViejas();
  // Al restaurar un rango del pasado las velas se acaban por la DERECHA: sin
  // esto, desplazarse hacia el presente enseñaba fondo vacío.
  if (!loadingNewer && !liveEdge && range.to >= bars.length - 150) cargarMasNuevas();
});

async function cargarMasViejas() {
  loadingOlder = true;
  const era = tf.name, primera = bars[0][0];
  try {
    const page = await fetchCandles({ to: primera, limit: 5000 });
    // Un cambio de timeframe a media carga deja `bars` vacío: sin esta guarda,
    // bars[0][0] reventaba con "cannot read properties of undefined".
    if (!bars.length || era !== tf.name || primera !== bars[0][0]) return;
    if (!page.length) { noMoreHistory = true; return; }
    const prev = chart.timeScale().getVisibleLogicalRange();
    bars = page.concat(bars);
    render(); // prepend histórico: setData aquí es el patrón correcto; el streaming usa update()
    chart.timeScale().setVisibleLogicalRange({ from: prev.from + page.length, to: prev.to + page.length });
  } finally { loadingOlder = false; }
}

async function cargarMasNuevas() {
  loadingNewer = true;
  const era = tf.name, ultima = bars[bars.length - 1][0];
  try {
    const page = await fetchCandles({ from: ultima, limit: 5000 });
    if (!bars.length || era !== tf.name || ultima !== bars[bars.length - 1][0]) return;
    const repetida = page.find(r => r[0] === ultima);
    if (repetida) bars[bars.length - 1] = repetida;   // el bucket del borde pudo quedar a medias
    const nuevas = page.filter(r => r[0] > ultima);
    // LWC guarda la posición como distancia a la ÚLTIMA vela, así que al
    // añadir por la derecha la vista se va con ellas: saltaba de marzo de 2020
    // a diciembre de 2025 sin que nadie la tocara. Se vuelve a fijar a mano.
    const prev = chart.timeScale().getVisibleLogicalRange();
    if (nuevas.length) bars = bars.concat(nuevas);
    render();
    if (prev) chart.timeScale().setVisibleLogicalRange(prev);
    if (!nuevas.length || bars[bars.length - 1][0] >= ahora() - 2 * barStep()) {
      liveEdge = true; resetStream();                 // ya estamos al día: vuelve el streaming
    }
  } finally { loadingNewer = false; }
}

// ---------- streaming (WS → agregación cliente al TF activo) ----------
// El servidor emite la vela de 1s en curso (~3/s). El cliente la funde en el
// bucket del TF activo; al cerrarse un bucket se re-pide a REST para dejarlo
// exacto (los buckets viejos ya vienen exactos del servidor).
let cur = null; // { t, o, h, l, c, vBase, lastSecT, lastSecV }
function resetStream() { cur = null; }

async function onTick(m) {
  // Mirando el pasado (F4-1.1) la vela de ahora no va detrás de la última
  // cargada: se ignora hasta volver al presente.
  if (!bars.length || !liveEdge) return;
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
  statusEl.textContent = estadoTexto(m.c.toFixed(1));
  legend.refresh();
}

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/api/ws`);
  ws.onmessage = (ev) => { try { onTick(JSON.parse(ev.data)); } catch {} };
  ws.onclose = () => { statusEl.textContent = 'reconectando…'; setTimeout(connectWS, 2000); };
}

// ---------- dibujos (F3: motor propio, ver draw/engine.js) ----------
// El paso por segundos-por-vela permite colocar dibujos entre velas y a la
// derecha de la última: el motor convierte tiempo <-> índice lógico.
function barStep() {
  if (tf.seconds > 0) return tf.seconds;
  if (bars.length > 1) return bars[bars.length - 1][0] - bars[bars.length - 2][0];
  return 86400 * 30;                       // meses de calendario: aproximación
}

const engine = new DrawEngine({
  chart, series, container,
  getBars: () => bars,
  getStep: barStep,
  autoscaleWithShapes: () => CONFIG.drawingsAutoscale,
  magnetPx: () => CONFIG.magnetPx,
  onFlags: () => sincronizarBotones(),
  onSave: (id, payload) => fetch(`/api/drawings/${id}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
  }).catch(() => {}),
  onDelete: (id) => fetch(`/api/drawings/${id}`, { method: 'DELETE' }).catch(() => {}),
  onSelect: () => sincronizarBotones(),
});

// Estado visible de la barra de herramientas: la herramienta activa y los
// interruptores que no son herramientas (imán).
function sincronizarBotones() {
  const activa = engine.activeTool();
  document.querySelectorAll('.tools button').forEach(b => {
    const t = b.dataset.tool;
    if (t === '__magnet') b.classList.toggle('active', engine.iman);
    else if (t === '__hide') b.classList.toggle('active', engine.ocultos);
    else if (t === '__lock') b.classList.toggle('active', engine.bloqueados);
    else b.classList.toggle('active', t === activa);
  });
}
mountPanel(engine, $('#drawPanel'));

// ---------- legend OHLC (F4-3.2) ----------
legend = mountLegend({
  chart, series, el: $('#legend'), getBars: () => bars, up: UP, down: DOWN,
});

async function loadDrawings() {
  const rows = await fetch('/api/drawings').then(r => r.json()).catch(() => []);
  engine.load(rows);
}

// ---------- toolbar ----------
const tfsEl = $('#tfs');
for (const t of TFS) {
  const b = document.createElement('button');
  b.textContent = t.name; b.dataset.tf = t.name;
  // blur(): si el botón se queda con el foco, las flechas de teclado (F4-1.2)
  // se las come él (moverían el scroll de la barra) en vez de cambiar de
  // timeframe. Es el mismo tipo de fallo que dejó a Supr sin efecto en F3.
  b.onclick = () => { b.blur(); loadTF(t); };
  tfsEl.appendChild(b);
}

// Flechas arriba/abajo: timeframe siguiente/anterior en el orden de la barra.
// A diferencia del click en la barra, que conserva el TRAMO, las flechas
// conservan el ancho de vela: sirven para acercarse y alejarse. En los
// extremos no hacen nada.
addEventListener('keydown', (e) => {
  if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
  if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
  const el = document.activeElement;
  // Dentro de un input o un select las flechas ya significan algo (mover un
  // deslizador, cambiar de opción): ahí no se tocan.
  if (el && (el.isContentEditable || ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName))) return;
  e.preventDefault();
  const i = TFS.findIndex(x => x.name === tf.name) + (e.key === 'ArrowUp' ? 1 : -1);
  if (i >= 0 && i < TFS.length) loadTF(TFS[i], { mismasVelas: true });
});

// End: volver al presente conservando el ancho de la ventana. Con 1.1 y 1.3 se
// puede acabar en 2020 y quedarse ahí también al recargar; hace falta una
// salida que no sea desplazarse a mano cinco años.
addEventListener('keydown', (e) => {
  if (e.key !== 'End' || e.ctrlKey || e.metaKey || e.altKey) return;
  const el = document.activeElement;
  if (el && (el.isContentEditable || ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName))) return;
  e.preventDefault();
  const r = chart.timeScale().getVisibleLogicalRange();
  loadTF(tf, { view: null, span: r ? r.to - r.from : undefined });
});
document.querySelectorAll('.tools button').forEach(b => {
  b.onclick = () => {
    const t = b.dataset.tool;
    if (t === '__clear') { engine.deleteSelected(); return; }
    if (t === '__measure') { engine.armMeasure(); return; }
    if (t === '__magnet') { b.blur(); engine.setIman(!engine.iman); return; }
    if (t === '__hide') { b.blur(); engine.setOcultos(!engine.ocultos); return; }
    if (t === '__lock') { b.blur(); engine.setBloqueados(!engine.bloqueados); return; }
    engine.setTool(engine.activeTool() === t ? null : t);
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

sincronizarBotones();   // el imán guardado tiene que verse encendido al entrar

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
  // Se vuelve al timeframe y la posición de la última sesión (F4-1.3). Si el
  // borde derecho estaba pegado al presente se conserva el ANCHO, no el
  // instante: al volver mañana lo interesante sigue siendo lo último.
  const v = vistaGuardada();
  const inicial = v ? TFS.find(t => t.name === v.tf) : tf;
  if (v && !v.live) await loadTF(inicial, { view: { from: v.from, to: v.to } });
  else if (v) await loadTF(inicial, { view: null, span: v.span });
  else await loadTF(tf, { view: null });
  await loadDrawings();
  connectWS();
})();

// hooks de test (DST, streaming) — sin efecto en producción
window.__test = { bucketStart, fmtTick, fmtFull, TFS, loadTF, chart, series,
  getBars: () => bars, getTF: () => tf, CONFIG, tfsEl, toolsEl, engine,
  panelEl: $('#drawPanel'),
  visibleTimeRange, vistaGuardada, VIEW_KEY, isLive: () => liveEdge };
