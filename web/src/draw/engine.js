// Motor de dibujos propio sobre la API de primitives de LWC v5.
//
// Por qué propio y no el plugin (justificación larga en el README): el plugin
// escuchaba el ratón DESPUÉS que el gráfico, así que al arrastrar en
// horizontal quien se movía era el gráfico. Aquí los eventos se capturan en
// fase de CAPTURA sobre el contenedor y, cuando el gesto es nuestro, se corta
// la propagación: LWC no llega a enterarse y no puede desplazar nada.
//
// El modelo guarda (tiempo UTC absoluto, precio) — NUNCA índices de barra —
// y la conversión a pantalla pasa por el índice lógico, extrapolando fuera
// del rango de datos para poder dibujar a la derecha de la última vela.
import { TYPES, DEFAULT_STYLE, drawHandles, drawSelection } from './shapes.js';
import { HANDLE, dist, rgba, fmtDuration, fmtPrice, fmtPct } from './geom.js';
import { logicalOf as aLogico, timeOfLogical as aTiempo } from '../timemap.js';
import { Estilos } from './styles.js';
import { History } from './history.js';

const PERSIST_MS = 400;
const uuid = () => crypto.randomUUID();

// Ajustes que viven en el navegador, como la posición de la barra.
const FLAG_IMAN = 'btcdash.iman';
const FLAG_OCULTOS = 'btcdash.dibujosOcultos';
const FLAG_BLOQUEADOS = 'btcdash.dibujosBloqueados';

export class DrawEngine {
  constructor({ chart, series, container, getBars, getStep, onSave, onDelete, onSelect,
    autoscaleWithShapes, magnetPx, onFlags }) {
    this.chart = chart;
    this.series = series;
    this.container = container;
    this.getBars = getBars;                 // () => filas [t,o,h,l,c,v]
    this.getStep = getStep;                 // () => segundos por vela del TF
    this.onSave = onSave || (() => {});
    this.onDelete = onDelete || (() => {});
    this.onSelect = onSelect || (() => {});
    this.autoscaleWithShapes = autoscaleWithShapes || (() => false);
    this.magnetPx = magnetPx || (() => 12);
    this.onFlags = onFlags || (() => {});
    this.iman = localStorage.getItem(FLAG_IMAN) === '1';               // F4-3.3
    this.ocultos = localStorage.getItem(FLAG_OCULTOS) === '1';         // F4-3.4
    this.bloqueados = localStorage.getItem(FLAG_BLOQUEADOS) === '1';

    this.shapes = [];
    this.estilos = new Estilos();           // estilo por defecto y plantillas (F4-2)
    this.history = new History(100);        // deshacer / rehacer (F4-3.1)
    this.selectedId = null;
    this.activeLine = 0;                    // en zone2, qué línea edita el panel
    this.pending = null;                    // creación en curso
    this.drag = null;
    this.measure = null;                    // {from:{time,price}, to, fixed}
    this.hover = null;
    this.cursor = null;                     // último (x,y) del ratón en el panel
    this.timers = new Map();
    this.requestUpdate = null;
    this._paneRect = null;

    this._installPrimitive();
    this._installEvents();
    this.history.init(this._state());
  }

  // ---------- deshacer / rehacer (F4-3.1) ----------
  _state() {
    return JSON.stringify(this.shapes.map(s => ({ id: s.id, ...this.toJSON(s) })));
  }

  // Cambios estructurales (crear, borrar, soltar un arrastre): entrada propia.
  _commit() {
    clearTimeout(this._histTimer);
    this.history.commit(this._state());
  }

  // Cambios de estilo: un deslizador dispara decenas de eventos por segundo y
  // no puede dejar decenas de pasos que deshacer uno a uno.
  _commitSoon() {
    clearTimeout(this._histTimer);
    this._histTimer = setTimeout(() => this.history.commit(this._state()), 500);
  }

  _apply(json) {
    const destino = JSON.parse(json);
    const vivos = new Set(destino.map(o => o.id));
    for (const s of this.shapes) {
      if (vivos.has(s.id)) continue;
      clearTimeout(this.timers.get(s.id));     // el guardado diferido resucitaría la figura
      this.timers.delete(s.id);
      this.onDelete(s.id);
    }
    this.shapes = destino.map(o => ({
      id: o.id, type: o.type,
      points: o.points.map(p => ({ t: p.t, p: p.p })),
      style: { ...DEFAULT_STYLE, ...o.style },
      ...(o.style2 ? { style2: { ...DEFAULT_STYLE, ...o.style2 } } : {}),
      ...(o.text !== undefined ? { text: o.text } : {}),
    }));
    if (!this.shapes.some(s => s.id === this.selectedId)) this.selectedId = null;
    for (const s of this.shapes) this.save(s);  // reescribe el servidor con lo que toca
    this.onSelect(this.selected(), this);
    this.redraw();
  }

  undo() { const st = this.history.undo(); if (st !== null) this._apply(st); return st !== null; }
  redo() { const st = this.history.redo(); if (st !== null) this._apply(st); return st !== null; }

  // ---------- conversión (tiempo, precio) <-> pantalla ----------
  // Se pasa por el índice lógico porque timeToCoordinate solo resuelve
  // tiempos que existen en los datos: con esto un dibujo puede vivir entre
  // dos velas o a la derecha de la última.
  logicalOf(time) { return aLogico(this.getBars(), this.getStep(), time); }
  timeOfLogical(l) { return aTiempo(this.getBars(), this.getStep(), l); }

  // OJO con la API de LWC: logicalToCoordinate(l) devuelve 0 si l no es
  // entero, y coordinateToLogical(x) redondea al índice de vela más cercano.
  // Usarlas tal cual cuantiza todo a velas enteras — exactamente el defecto
  // que le achacamos al plugin. Las dos funciones de aquí interpolan entre
  // dos anclas enteras, que sí resuelve bien, para trabajar con fracciones.
  logicalOfX(x) {
    const ts = this.chart.timeScale();
    const i = ts.coordinateToLogical(x);
    if (i === null) return null;
    const xi = ts.logicalToCoordinate(i), xi1 = ts.logicalToCoordinate(i + 1);
    if (xi === null || xi1 === null || xi1 === xi) return i;
    return i + (x - xi) / (xi1 - xi);
  }

  xOf(time) {
    const l = this.logicalOf(time);
    if (l === null) return null;
    const ts = this.chart.timeScale();
    const i = Math.floor(l), f = l - i;
    const xi = ts.logicalToCoordinate(i);
    if (xi === null) return null;
    if (!f) return xi;
    const xi1 = ts.logicalToCoordinate(i + 1);
    return xi1 === null ? xi : xi + f * (xi1 - xi);
  }

  timeOfX(x) {
    const l = this.logicalOfX(x);
    return l === null ? null : this.timeOfLogical(l);
  }

  yOf(price) { return this.series.priceToCoordinate(price); }
  priceOfY(y) { return this.series.coordinateToPrice(y); }

  screenPoints(s) {
    const out = [];
    for (const p of s.points) {
      const x = this.xOf(p.t), y = this.yOf(p.p);
      if (x === null || y === null) return null;
      out.push({ x, y });
    }
    return out;
  }

  // ---------- primitive ----------
  _installPrimitive() {
    const self = this;
    const paneView = {
      // 'normal' = por encima de las velas pero POR DEBAJO del crosshair (con
      // 'top' un relleno opaco se comía la cruz). De paso, el lienzo del panel
      // deja de repintarse en cada movimiento del ratón.
      zOrder: () => 'normal',
      renderer: () => ({
        draw(target) {
          target.useMediaCoordinateSpace(({ context, mediaSize }) => {
            self._paint(context, mediaSize);
          });
        },
      }),
    };
    this._paneViews = [paneView];
    this._axisViews = [];

    this.primitive = {
      attached: (p) => { self.requestUpdate = p.requestUpdate; },
      detached: () => { self.requestUpdate = null; },
      updateAllViews: () => self._syncAxisViews(),
      // Los dibujos NO estiran la escala: al pulsar AUTO solo mandan las
      // velas (RF-5.10). Queda la palanca para poder demostrar en el test que
      // la primitive SÍ está siendo consultada — si no, el check pasaría
      // igual aunque estuviera desconectada.
      autoscaleInfo: (from, to) => {
        if (!self.autoscaleWithShapes || !self.autoscaleWithShapes()) return null;
        let min = Infinity, max = -Infinity;
        for (const x of self.shapes) {
          // Las horizontales se pintan de lado a lado: cuentan siempre. El
          // resto, solo si caen dentro del rango visible que nos pasan (si no,
          // una figura lejana aplastaría las velas para siempre).
          const anchas = TYPES[x.type].priceLabel;
          for (const q of x.points) {
            const l = self.logicalOf(q.t);
            if (!anchas && (l === null || l < from || l > to)) continue;
            if (q.p < min) min = q.p;
            if (q.p > max) max = q.p;
          }
        }
        return min <= max ? { priceRange: { minValue: min, maxValue: max } } : null;
      },
      paneViews: () => self._paneViews,
      priceAxisViews: () => self._axisViews,
      hitTest: (x, y) => {
        const hit = self._shapeAt(x, y);
        if (!hit) return null;
        return { externalId: hit.id, zOrder: 'top', cursorStyle: 'move', hitTestPriority: 1 };
      },
    };
    this.series.attachPrimitive(this.primitive);
  }

  redraw() { if (this.requestUpdate) this.requestUpdate(); }

  _paint(ctx, mediaSize) {
    const env = {
      width: mediaSize.width,
      height: mediaSize.height,
      measure: (t, size) => { ctx.font = `${size}px system-ui, sans-serif`; return ctx.measureText(t).width; },
    };
    ctx.save();
    // Ocultos: se salta el dibujo entero, pero la medición se sigue pintando
    // (es una herramienta del momento, no un dibujo guardado).
    if (!this.ocultos) for (const s of this.shapes) {
      const pts = this.screenPoints(s);
      if (!pts) continue;
      if (s.id === this.selectedId) drawSelection(ctx, s, pts, env);
      TYPES[s.type].draw(ctx, s, pts, env);
    }
    // vista previa de la figura que se está creando
    if (this.pending && this.cursor) {
      const prev = this._pendingPreview();
      if (prev) {
        const pts = this.screenPoints(prev);
        if (pts) { ctx.globalAlpha = 0.7; TYPES[prev.type].draw(ctx, prev, pts, env); ctx.globalAlpha = 1; }
      }
    }
    const sel = this.ocultos ? null : this.selected();
    if (sel) {
      const pts = this.screenPoints(sel);
      if (pts) drawHandles(ctx, TYPES[sel.type].handles(pts));
    }
    if (this.measure) this._paintMeasure(ctx, env);
    ctx.restore();
    this._syncAxisViews();
    if (this.onRender) this.onRender();
  }

  // Etiqueta de precio en la escala derecha para las figuras horizontales.
  _syncAxisViews() {
    const views = [];
    for (const s of this.shapes) {
      const t = TYPES[s.type];
      if (!t.priceLabel) continue;
      s.points.forEach((p, i) => {
        const style = i === 1 && s.style2 ? s.style2 : s.style;
        const y = this.yOf(p.p);
        if (y === null) return;
        views.push({
          // viva, no capturada: si no, la etiqueta va un repintado por detrás
          coordinate: () => this.yOf(p.p) ?? y,
          text: () => fmtPrice(p.p),
          textColor: () => '#111111',
          backColor: () => style.color,
        });
      });
    }
    this._axisViews = views;
  }

  // ---------- eventos ----------
  _installEvents() {
    const c = this.container;
    // CAPTURA: llegamos antes que los listeners internos de LWC, que cuelgan
    // del canvas. Si el gesto es nuestro, stopPropagation y el gráfico ni se
    // entera; si no lo es, lo dejamos pasar y el gráfico se desplaza normal.
    c.addEventListener('pointerdown', (e) => this._onDown(e), true);
    c.addEventListener('pointermove', (e) => this._onMoveHover(e), true);
    addEventListener('pointermove', (e) => this._onDrag(e), true);
    addEventListener('pointerup', (e) => this._onUp(e), true);
    // Un gesto puede morir sin pointerup (Alt+Tab con el botón pulsado, o un
    // pointercancel del navegador). Sin esto el arrastre seguía vivo y la
    // figura se movía con el ratón SIN botón hasta el siguiente click.
    addEventListener('pointercancel', () => this._onUp(), true);
    addEventListener('blur', () => this._onUp());
    addEventListener('keydown', (e) => this._onKey(e));
    addEventListener('resize', () => { this._paneRect = null; });
    c.addEventListener('contextmenu', (e) => {
      if (this.pending) { e.preventDefault(); this.setTool(null); }
    });
  }

  // Rectángulo del canvas del panel (no del contenedor: fuera quedan las
  // escalas de precio y tiempo, y las coordenadas de LWC son del panel).
  paneRect() {
    if (!this._paneRect) {
      const cs = [...this.container.querySelectorAll('canvas')];
      if (!cs.length) return null;
      const big = cs.reduce((a, b) => (a.width * a.height >= b.width * b.height ? a : b));
      this._paneRect = big.getBoundingClientRect();
    }
    return this._paneRect;
  }

  _pos(e, fresh = false) {
    if (fresh) this._paneRect = null;
    const r = this.paneRect();
    if (!r) return null;
    return { x: e.clientX - r.left, y: e.clientY - r.top, inside: e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom };
  }

  _shapeAt(x, y) {
    if (this.inertes) return null;
    const env = { measure: (t, size) => measureText(t, size) };
    for (let i = this.shapes.length - 1; i >= 0; i--) {   // el de arriba manda
      const s = this.shapes[i];
      const pts = this.screenPoints(s);
      if (pts && TYPES[s.type].hit(x, y, s, pts, env)) return s;
    }
    return null;
  }

  _handleAt(x, y) {
    if (this.inertes) return -1;
    const s = this.selected();
    if (!s) return -1;
    const pts = this.screenPoints(s);
    if (!pts) return -1;
    const hs = TYPES[s.type].handles(pts);
    for (let i = 0; i < hs.length; i++) {
      if (Math.abs(x - hs[i].x) <= HANDLE + 3 && Math.abs(y - hs[i].y) <= HANDLE + 3) return i;
    }
    return -1;
  }

  _onDown(e) {
    if (e.button !== 0) return;
    const p = this._pos(e, true);   // al pulsar sí se remide: el layout pudo cambiar
    if (!p || !p.inside) return;
    // Cortar la propagación basta para que LWC no vea el gesto; el candado
    // del paneo se echa solo al empezar un arrastre de verdad (_startDrag).
    const take = () => { e.preventDefault(); e.stopPropagation(); };

    // 1) creación en curso
    if (this.pending) { take(); this._addPendingPoint(p); return; }

    // 2) medición: shift+click arranca; con medición viva, un click la fija;
    //    con medición fija, un click la borra. Excepción: shift sobre un
    //    handle de la zona ajusta esa línea (ver _startDrag).
    const handleIdx = this._handleAt(p.x, p.y);
    if (this.measure) {
      take();
      if (this.measure.fixed) this.measure = null; else this.measure.fixed = true;
      this.redraw();
      return;
    }
    if ((e.shiftKey || this.armed) && handleIdx < 0) {
      this.armed = false;
      take();
      this.measure = { from: this._pt(p), to: this._pt(p), fixed: false };
      this.redraw();
      return;
    }

    // 3) redimensionar por un punto de control
    if (handleIdx >= 0) { take(); this._startDrag('resize', handleIdx, p, e.shiftKey); return; }

    // 4) seleccionar y mover. Con Alt se arrastra una COPIA y el original se
    //    queda donde estaba (F4-3.5).
    const hit = this._shapeAt(p.x, p.y);
    if (hit) {
      take();
      const objetivo = e.altKey ? this._duplicar(this.toJSON(hit)) : hit;
      this._select(objetivo.id, p);
      this._startDrag('move', -1, p, e.shiftKey);
      return;
    }
    // 5) fuera de todo: deseleccionar y DEJAR PASAR el evento (el gráfico se
    //    desplaza como siempre).
    this._select(null);
  }

  // Un punto solo vale si las dos conversiones han salido bien: si no, se
  // acabaría guardando {t:null,p:null}, que al recargar rompe el pintado.
  _pt(q) {
    if (this.iman) {
      const m = this._imanar(q.x, q.y);
      if (m) return { t: m.t, p: m.p };
    }
    const t = this.timeOfX(q.x), p = this.priceOfY(q.y);
    return Number.isFinite(t) && Number.isFinite(p) ? { t, p } : null;
  }

  // ---------- imán (F4-3.3) ----------
  // Engancha el punto al open/high/low/close de la vela más cercana si cae a
  // menos de magnetPx() píxeles. Devuelve también el TIEMPO de esa vela: un
  // nivel marcado en un máximo tiene que caer sobre el máximo, no al lado.
  _imanar(x, y) {
    const bars = this.getBars();
    if (!bars.length) return null;
    const l = this.logicalOfX(x);
    if (l === null) return null;
    const centro = Math.max(0, Math.min(bars.length - 1, Math.round(l)));
    const tol = this.magnetPx();
    let mejor = null;
    // La vela de al lado también entra: alejado el gráfico una vela mide un
    // píxel, y clavar el cursor en la de en medio es imposible. Manda la
    // distancia vertical, que es la que se ve.
    for (let i = Math.max(0, centro - 1); i <= Math.min(bars.length - 1, centro + 1); i++) {
      const b = bars[i];
      for (const v of [b[1], b[2], b[3], b[4]]) {
        const yv = this.yOf(v);
        if (yv === null) continue;
        const d = Math.abs(yv - y);
        if (d <= tol && (!mejor || d < mejor.d || (d === mejor.d && i === centro))) {
          mejor = { d, t: b[0], p: v };
        }
      }
    }
    return mejor;
  }

  setIman(on) {
    this.iman = !!on;
    localStorage.setItem(FLAG_IMAN, this.iman ? '1' : '0');
    this.onFlags(this);
  }

  // ---------- ocultar y bloquear (F4-3.4) ----------
  // Ocultos: no se pintan ni se pueden tocar — el precio, limpio.
  // Bloqueados: se ven pero son inertes; el ratón atraviesa y el gráfico se
  // desplaza como si no estuvieran, que es de lo que protege el candado.
  setOcultos(on) {
    this.ocultos = !!on;
    localStorage.setItem(FLAG_OCULTOS, this.ocultos ? '1' : '0');
    if (this.ocultos) this._select(null);
    this.onFlags(this);
    this.redraw();
  }

  setBloqueados(on) {
    this.bloqueados = !!on;
    localStorage.setItem(FLAG_BLOQUEADOS, this.bloqueados ? '1' : '0');
    if (this.bloqueados) this._select(null);
    this.onFlags(this);
    this.redraw();
  }

  // Ni selección ni arrastre ni hit-test mientras estén ocultos o bloqueados.
  get inertes() { return this.ocultos || this.bloqueados; }

  static valido(s) {
    return Array.isArray(s.points) && s.points.length > 0
      && s.points.every(q => Number.isFinite(q.t) && Number.isFinite(q.p));
  }

  _startDrag(mode, idx, p, shift) {
    const s = this.selected();
    if (!s) return;
    this._lockChart(true);
    this.drag = {
      mode, idx, shift,
      x0: p.x, y0: p.y,
      l0: this.logicalOfX(p.x),
      snap: s.points.map(q => ({ l: this.logicalOf(q.t), y: this.yOf(q.p) })),
    };
  }

  _onDrag(e) {
    if (!this.drag) return;
    if (e.buttons === 0) { this._onUp(); return; }   // el botón ya no está pulsado
    const s = this.selected();
    if (!s) return;
    e.preventDefault(); e.stopPropagation();
    const r = this.paneRect();
    const x = e.clientX - r.left, y = e.clientY - r.top;
    const dl = this.logicalOfX(x) - this.drag.l0;
    const dy = y - this.drag.y0;
    const type = TYPES[s.type];

    const move = (i) => {
      const snap = this.drag.snap[i];
      const t = this.timeOfLogical(snap.l + dl), pr = this.priceOfY(snap.y + dy);
      if (Number.isFinite(t) && Number.isFinite(pr)) s.points[i] = { t, p: pr };
    };
    if (this.drag.mode === 'move') {
      s.points.forEach((_, i) => move(i));
    } else if (type.linked && !this.drag.shift) {
      // zona: arrastrar un extremo mueve las DOS manteniendo la distancia
      s.points.forEach((_, i) => move(i));
    } else {
      // Un punto de control puede no corresponder 1 a 1 con un punto del
      // modelo (el rectángulo tiene 4 esquinas y 2 puntos).
      const targets = type.handleTargets
        ? type.handleTargets(this.drag.idx)
        : [{ i: this.drag.idx, axes: 'xy' }];
      // Con el imán, un punto de control se engancha igual que al dibujarlo.
      const m = this.iman ? this._imanar(x, y) : null;
      for (const tg of targets) {
        const snap = this.drag.snap[tg.i];
        if (!snap) continue;
        const q = s.points[tg.i];
        const t = m ? m.t : this.timeOfLogical(snap.l + dl);
        const pr = m ? m.p : this.priceOfY(snap.y + dy);
        if (tg.axes.includes('x') && Number.isFinite(t)) q.t = t;
        if (tg.axes.includes('y') && Number.isFinite(pr)) q.p = pr;
      }
      if (type.linked) {   // el tiempo de las dos líneas de la zona va unido
        const t = s.points[this.drag.idx].t;
        s.points.forEach((q) => { q.t = t; });
      }
    }
    this.redraw();
  }

  _onUp() {
    // Siempre se suelta el candado, aunque no hubiera arrastre: si no, un
    // simple click de creación dejaba el gráfico sin paneo para siempre.
    this._lockChart(false);
    if (!this.drag) return;
    const s = this.selected();
    this.drag = null;
    if (s) { this.save(s); this._commit(); }   // mover y redimensionar se deshacen
  }

  _onMoveHover(e) {
    const p = this._pos(e);
    if (!p) return;
    this.cursor = p;
    if (this.pending || this.measure) { this.redraw(); return; }
  }

  _onKey(e) {
    // "Escribiendo" es solo un campo de texto: los sliders y el selector de
    // color son <input> igualmente, y con ellos Supr debe seguir borrando.
    const el = document.activeElement;
    const escribiendo = !!el && (el.isContentEditable || el.tagName === 'TEXTAREA'
      || (el.tagName === 'INPUT' && /^(text|search|url|email|number|password|tel)$/.test(el.type)));
    if (e.key === 'Escape') {
      if (escribiendo) { el.blur(); return; }
      if (this.pending) this.setTool(null);
      else if (this.armed) { this.armed = false; this.container.style.cursor = ''; }
      else if (this.measure) { this.measure = null; this.redraw(); }
      else this._select(null);
      return;
    }
    if ((e.key === 'Delete' || e.key === 'Backspace') && !escribiendo) this.deleteSelected();
    // Los atajos de una sola letra se apagan dentro de CUALQUIER control de
    // formulario, no solo de los campos de texto: un <select> abierto usa las
    // letras para buscar entre sus opciones (las plantillas tienen nombre).
    const enControl = !!el && (el.isContentEditable
      || ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName));
    if (!enControl && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (e.key === 'm' || e.key === 'M') { this.setIman(!this.iman); return; }
      if (e.key === 'h' || e.key === 'H') { this.setOcultos(!this.ocultos); return; }
      if (e.key === 'l' || e.key === 'L') { this.setBloqueados(!this.bloqueados); return; }
    }
    // Ctrl+Z / Ctrl+Shift+Z / Ctrl+Y. Escribiendo en un campo manda el
    // deshacer del navegador, que es lo que espera cualquiera.
    const mod = e.ctrlKey || e.metaKey;
    // Copiar y pegar solo se secuestran con una figura seleccionada o algo
    // copiado antes: sin eso, el Ctrl+C del navegador sigue siendo el suyo.
    if (mod && !escribiendo && (e.key === 'c' || e.key === 'C') && this.selected()) {
      e.preventDefault(); this.copiar(); return;
    }
    if (mod && !escribiendo && (e.key === 'v' || e.key === 'V') && this.clip) {
      e.preventDefault(); this.pegar(); return;
    }
    if (mod && !escribiendo && (e.key === 'z' || e.key === 'Z')) {
      e.preventDefault();
      if (e.shiftKey) this.redo(); else this.undo();
    } else if (mod && !escribiendo && (e.key === 'y' || e.key === 'Y')) {
      e.preventDefault();
      this.redo();
    }
  }

  // Mientras arrastramos un dibujo, el gráfico no se desplaza ni con el ratón
  // ni con inercia. Es redundante con el stopPropagation, pero cubre el caso
  // de que LWC escuche el movimiento en document.
  _lockChart(on) {
    if (this.locked === on) return;
    this.locked = on;
    this.chart.applyOptions(on
      ? { handleScroll: false }
      : { handleScroll: { mouseWheel: true, pressedMouseMove: true, horzTouchDrag: true, vertTouchDrag: true } });
  }

  // ---------- creación ----------
  setTool(type) {
    this.armed = false;              // elegir herramienta cancela el modo medir
    if (type && this.ocultos) this.setOcultos(false);   // dibujar a ciegas, no
    this.pending = type ? { type, pts: [] } : null;
    this.container.style.cursor = type ? 'crosshair' : '';
    // Al empezar a dibujar se deselecciona: si no, el panel de configuración
    // se queda encima del gráfico y se traga los clicks de creación.
    if (type) this.selectedId = null;
    this.onSelect(this.selected(), this);      // refresca panel y barra
    this.redraw();
  }

  activeTool() { return this.pending ? this.pending.type : null; }

  // Igual que shift+click, pero desde el botón de la barra: la medición
  // arranca en el siguiente click sobre el gráfico.
  armMeasure() {
    this.setTool(null);              // primero limpia, después arma
    this.armed = true;
    this.container.style.cursor = 'crosshair';
  }

  _addPendingPoint(p) {
    const { type } = this.pending;
    const punto = this._pt(p);
    if (!punto) return;              // conversión imposible: se ignora el click
    this.pending.pts.push(punto);
    const need = TYPES[type].points;
    if (this.pending.pts.length < need) { this.redraw(); return; }
    const points = this.pending.pts.slice();
    this.pending = null;
    this.container.style.cursor = '';
    const s = this.addShape(type, points);
    this._select(s.id);
  }

  // Alta de una figura (la usan la creación por clicks y los tests).
  addShape(type, points, extra = {}) {
    if (TYPES[type].linked) points.forEach(q => { q.t = points[0].t; });
    // El estilo por defecto es el último que se aplicó a esta herramienta
    // (F4-2.1), no una constante: es lo que espera quien dibuja diez zonas
    // seguidas del mismo color.
    const base = this.estilos.para(type);
    const s = {
      id: extra.id || uuid(), type,
      points: points.map(q => ({ t: q.t, p: q.p })),
      style: { ...base.style, ...(extra.style || {}) },
      ...(TYPES[type].linked ? { style2: { ...base.style2, ...(extra.style2 || {}) } } : {}),
      ...(TYPES[type].text ? { text: extra.text ?? 'Texto' } : {}),
    };
    this.shapes.push(s);
    this.redraw();
    this.save(s);
    this._commit();
    return s;
  }

  _pendingPreview() {
    const { type, pts } = this.pending;
    const need = TYPES[type].points;
    const all = [...pts, this._pt(this.cursor) || pts[pts.length - 1]];
    while (all.length < need) all.push(all[all.length - 1]);
    const points = all.slice(0, need);
    if (TYPES[type].linked) points.forEach(q => { q.t = points[0].t; });
    const base = this.estilos.para(type);
    return { id: '__preview', type, points, style: base.style, style2: base.style2, text: 'Texto' };
  }

  // ---------- selección y estilo ----------
  selected() { return this.shapes.find(s => s.id === this.selectedId) || null; }

  _select(id, p) {
    this.selectedId = id;
    if (id && p) {
      const s = this.selected();
      // en la zona, el panel edita la línea que se ha tocado
      if (s && TYPES[s.type].linked) {
        const pts = this.screenPoints(s);
        this.activeLine = pts && Math.abs(p.y - pts[1].y) < Math.abs(p.y - pts[0].y) ? 1 : 0;
      } else this.activeLine = 0;
    }
    this.onSelect(this.selected(), this);
    this.redraw();
  }

  select(id) { this._select(id); }

  styleOf(s = this.selected()) {
    if (!s) return null;
    return this.activeLine === 1 && s.style2 ? s.style2 : s.style;
  }

  patchStyle(patch) {
    const s = this.selected();
    if (!s) return;
    Object.assign(this.styleOf(s), patch);
    this.estilos.recordar(s.type, s.style, s.style2);
    this.redraw();
    this.save(s);
    this._commitSoon();
  }

  // ---------- plantillas de estilo (F4-2.2) ----------
  // Se aplican a la figura YA seleccionada, no solo a las nuevas, y de paso
  // pasan a ser el estilo por defecto de esa herramienta: aplicar una
  // plantilla es una modificación de estilo como cualquier otra (F4-2.1).
  aplicarPlantilla(nombre) {
    const s = this.selected(), t = this.estilos.get(nombre);
    if (!s || !t) return false;
    Object.assign(s.style, t.style);
    if (s.style2) Object.assign(s.style2, t.style2 || t.style);
    this.estilos.recordar(s.type, s.style, s.style2);
    this.redraw();
    this.save(s);
    this.onSelect(s, this);
    this._commitSoon();
    return true;
  }

  guardarPlantilla(nombre) {
    const s = this.selected();
    if (!s) return null;
    return this.estilos.guardar(nombre, s.style, s.style2);
  }

  setText(text) {
    const s = this.selected();
    if (!s) return;
    s.text = text;
    this.redraw();
    this.save(s);
    this._commitSoon();
  }

  deleteSelected() { this.remove(this.selectedId); }

  // ---------- duplicar (F4-3.5) ----------
  // El portapapeles es del gráfico, no del sistema: copiar un dibujo no puede
  // pisar lo que el usuario tenga copiado fuera.
  copiar() {
    const s = this.selected();
    if (!s) return false;
    this.clip = this.toJSON(s);
    return true;
  }

  pegar() {
    if (!this.clip) return null;
    // Desplazada 20 px: encima del original no se vería que hay dos.
    const s = this._duplicar(this.clip, 20, 20);
    if (s) this._select(s.id);
    return s;
  }

  _duplicar(json, dx = 0, dy = 0) {
    const points = json.points.map(q => {
      if (!dx && !dy) return { t: q.t, p: q.p };
      const x = this.xOf(q.t), y = this.yOf(q.p);
      if (x === null || y === null) return { t: q.t, p: q.p };
      const t = this.timeOfX(x + dx), pr = this.priceOfY(y + dy);
      return Number.isFinite(t) && Number.isFinite(pr) ? { t, p: pr } : { t: q.t, p: q.p };
    });
    return this.addShape(json.type, points,
      { style: json.style, style2: json.style2, text: json.text });
  }

  // Toda baja pasa por aquí: primero se cancela el guardado diferido y solo
  // después se pide el borrado. Si no, el debounce escribía la figura DESPUÉS
  // del DELETE y reaparecía en la siguiente recarga.
  remove(id) {
    if (!id || !this.shapes.some(x => x.id === id)) return;
    clearTimeout(this.timers.get(id));
    this.timers.delete(id);
    this.shapes = this.shapes.filter(x => x.id !== id);
    if (this.selectedId === id) this.selectedId = null;
    this.onDelete(id);
    this.onSelect(this.selected(), this);
    this.redraw();
    this._commit();
  }

  clear() { for (const s of [...this.shapes]) this.remove(s.id); }

  // ---------- persistencia ----------
  save(s) {
    clearTimeout(this.timers.get(s.id));
    this.timers.set(s.id, setTimeout(() => {
      // Segunda red: la figura pudo borrarse mientras el debounce esperaba.
      if (!this.shapes.some(x => x.id === s.id)) return;
      if (!DrawEngine.valido(s)) return;         // nunca se guarda un punto roto
      this.onSave(s.id, this.toJSON(s));
    }, PERSIST_MS));
  }

  toJSON(s) {
    return {
      kind: 'shape', v: 1, type: s.type,
      points: s.points.map(p => ({ t: p.t, p: p.p })),
      style: { ...s.style },
      ...(s.style2 ? { style2: { ...s.style2 } } : {}),
      ...(s.text !== undefined ? { text: s.text } : {}),
    };
  }

  load(rows) {
    for (const { id, payload } of rows) {
      if (!payload || payload.kind !== 'shape' || !TYPES[payload.type]) continue;
      if (!DrawEngine.valido(payload)) continue;   // fila corrupta: se ignora
      this.shapes.push({
        id, type: payload.type,
        points: payload.points.map(p => ({ t: p.t, p: p.p })),
        style: { ...DEFAULT_STYLE, ...payload.style },
        ...(payload.style2 ? { style2: { ...DEFAULT_STYLE, ...payload.style2 } } : {}),
        ...(payload.text !== undefined ? { text: payload.text } : {}),
      });
    }
    this.redraw();
    this.history.init(this._state());   // deshacer no puede llegar a antes de cargar
  }

  // ---------- medición ----------
  _paintMeasure(ctx, env) {
    const m = this.measure;
    if (!m.fixed && this.cursor) m.to = this._pt(this.cursor);
    const x1 = this.xOf(m.from.t), y1 = this.yOf(m.from.p);
    const x2 = this.xOf(m.to.t), y2 = this.yOf(m.to.p);
    if ([x1, y1, x2, y2].some(v => v === null)) return;

    const up = m.to.p >= m.from.p;
    const col = up ? '#26a69a' : '#ef5350';
    ctx.setLineDash([]);
    ctx.fillStyle = rgba(col, 0.18);
    ctx.fillRect(Math.min(x1, x2), Math.min(y1, y2), Math.abs(x2 - x1), Math.abs(y2 - y1));
    ctx.strokeStyle = rgba(col, 0.9);
    ctx.lineWidth = 1;
    ctx.strokeRect(Math.min(x1, x2), Math.min(y1, y2), Math.abs(x2 - x1), Math.abs(y2 - y1));
    arrow(ctx, x1, (y1 + y2) / 2, x2, (y1 + y2) / 2, col);   // horizontal
    arrow(ctx, (x1 + x2) / 2, y1, (x1 + x2) / 2, y2, col);   // vertical

    const d = m.to.p - m.from.p;
    const pct = m.from.p ? (d / m.from.p) * 100 : 0;
    const barsN = Math.round(Math.abs(this.logicalOf(m.to.t) - this.logicalOf(m.from.t)));
    const secs = Math.abs(m.to.t - m.from.t);
    const txt = [
      `${d >= 0 ? '+' : '−'}${fmtPrice(Math.abs(d))} (${pct >= 0 ? '+' : '−'}${fmtPct(Math.abs(pct))}%)`,
      `${barsN} barras · ${fmtDuration(secs)}`,
    ];
    ctx.font = '12px system-ui, sans-serif';
    const w = Math.max(...txt.map(t => ctx.measureText(t).width)) + 12;
    const bx = Math.max(2, Math.min((x1 + x2) / 2 - w / 2, env.width - w - 2));
    const by = Math.max(2, Math.min(Math.min(y1, y2) - 40, env.height - 36));
    ctx.fillStyle = rgba(col, 0.95);
    ctx.fillRect(bx, by, w, 34);
    ctx.fillStyle = '#ffffff';
    ctx.textBaseline = 'middle';
    txt.forEach((t, i) => ctx.fillText(t, bx + 6, by + 10 + i * 15));
  }

  measureInfo() {   // para los tests: lo mismo que se pinta, en datos
    const m = this.measure;
    if (!m) return null;
    const d = m.to.p - m.from.p;
    return {
      fixed: m.fixed, delta: d,
      pct: m.from.p ? (d / m.from.p) * 100 : 0,
      bars: Math.round(Math.abs(this.logicalOf(m.to.t) - this.logicalOf(m.from.t))),
      seconds: Math.abs(m.to.t - m.from.t),
      duration: fmtDuration(Math.abs(m.to.t - m.from.t)),
    };
  }
}

function arrow(ctx, x1, y1, x2, y2, col) {
  ctx.strokeStyle = rgba(col, 0.9);
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2); ctx.stroke();
  const a = Math.atan2(y2 - y1, x2 - x1), h = 6;
  if (dist(x1, y1, x2, y2) < 4) return;
  ctx.beginPath();
  ctx.moveTo(x2, y2);
  ctx.lineTo(x2 - h * Math.cos(a - 0.4), y2 - h * Math.sin(a - 0.4));
  ctx.lineTo(x2 - h * Math.cos(a + 0.4), y2 - h * Math.sin(a + 0.4));
  ctx.closePath();
  ctx.fillStyle = rgba(col, 0.9);
  ctx.fill();
}

// medición de texto fuera del canvas del gráfico (para hit-test)
let _mctx = null;
function measureText(t, size) {
  if (!_mctx) _mctx = document.createElement('canvas').getContext('2d');
  _mctx.font = `${size}px system-ui, sans-serif`;
  return _mctx.measureText(t).width;
}
