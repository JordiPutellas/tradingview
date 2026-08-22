// Alertas de precio en el gráfico (F5, bloque 3).
//
// Aquí solo hay interfaz: crear, ver, editar y borrar. La evaluación es del
// servidor (proceso `alerts`), que corre con el ordenador apagado — que es
// justo lo que las hace útiles y lo que ata a la suscripción de TradingView.
//
// Las alertas se pintan con una primitive propia, aparte de la del motor de
// dibujos: son cosas distintas y no deben compartir estado ni z-order.
import { rgba, fmtPrice } from './draw/geom.js';

const COLOR = '#e0a030';           // ámbar: ni el azul de las velas ni el gris
const COLOR_APAGADA = '#6b6b6b';

export function mountAlerts({ chart, series, container, panelEl, cfg }) {
  let alertas = [];
  let requestUpdate = null;
  let paneRect = null;

  const api = {
    listar: () => fetch('/api/alerts').then(r => r.json()).catch(() => []),
    guardar: (a) => fetch(`/api/alerts/${a.id}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(a),
    }),
    borrar: (id) => fetch(`/api/alerts/${id}`, { method: 'DELETE' }),
    estado: () => fetch('/api/alerts/status').then(r => r.json()).catch(() => ({})),
    probar: () => fetch('/api/alerts/test', { method: 'POST' }),
  };

  // ---------- pintado ----------
  const primitive = {
    attached: (p) => { requestUpdate = p.requestUpdate; },
    detached: () => { requestUpdate = null; },
    updateAllViews: () => {},
    // Las alertas NO estiran la escala: igual que los dibujos (RF-5.10), una
    // alerta lejana no puede aplastar las velas.
    autoscaleInfo: () => null,
    paneViews: () => [{
      zOrder: () => 'normal',
      renderer: () => ({
        draw(target) {
          target.useMediaCoordinateSpace(({ context, mediaSize }) => pintar(context, mediaSize));
        },
      }),
    }],
    priceAxisViews: () => alertas.filter(a => a.status === 'armed').map(a => ({
      coordinate: () => series.priceToCoordinate(a.level) ?? -100,
      text: () => fmtPrice(a.level),
      textColor: () => '#111111',
      backColor: () => COLOR,
    })),
  };
  series.attachPrimitive(primitive);

  function pintar(ctx, size) {
    ctx.save();
    for (const a of alertas) {
      if (a.status === 'done') continue;
      const y = series.priceToCoordinate(a.level);
      if (y === null) continue;
      const activa = a.status === 'armed';
      ctx.strokeStyle = rgba(activa ? COLOR : COLOR_APAGADA, activa ? 0.95 : 0.5);
      ctx.lineWidth = 1;
      ctx.setLineDash([2, 4]);          // discontinua fina: no compite con los dibujos
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(size.width, y);
      ctx.stroke();
      ctx.setLineDash([]);
      const flecha = a.direction === 'up' ? '▲' : a.direction === 'down' ? '▼' : '⇅';
      const txt = `🔔 ${flecha} ${fmtPrice(a.level)}${a.note ? ' · ' + a.note : ''}`;
      ctx.font = '11px system-ui, sans-serif';
      const w = ctx.measureText(txt).width + 10;
      ctx.fillStyle = rgba(activa ? COLOR : COLOR_APAGADA, 0.9);
      ctx.fillRect(4, y - 15, w, 13);
      ctx.fillStyle = '#111111';
      ctx.textBaseline = 'middle';
      ctx.fillText(txt, 9, y - 8);
    }
    ctx.restore();
  }

  const repintar = () => { if (requestUpdate) requestUpdate(); };

  // ---------- estado ----------
  async function recargar() {
    alertas = await api.listar();
    repintar();
    pintarPanel();
  }

  function rectPanel() {
    if (!paneRect) {
      const cs = [...container.querySelectorAll('canvas')];
      if (!cs.length) return null;
      const big = cs.reduce((a, b) => (a.width * a.height >= b.width * b.height ? a : b));
      paneRect = big.getBoundingClientRect();
    }
    return paneRect;
  }
  addEventListener('resize', () => { paneRect = null; });

  function precioEn(clientY) {
    paneRect = null;                       // el layout pudo cambiar
    const r = rectPanel();
    if (!r) return null;
    const p = series.coordinateToPrice(clientY - r.top);
    return Number.isFinite(p) ? p : null;
  }

  async function crear(nivel, direction = 'any', extra = {}) {
    const a = {
      id: crypto.randomUUID(), level: nivel, direction, mode: 'once', status: 'armed',
      note: '', rearm_bps: 5, cooldown_sec: cfg?.alertCooldown ?? 300, max_per_day: 20, ...extra,
    };
    await api.guardar(a);
    await recargar();
    return a;
  }

  // ---------- menú contextual ----------
  const menu = document.createElement('div');
  menu.id = 'alertMenu';
  menu.hidden = true;
  document.body.appendChild(menu);

  function cerrarMenu() { menu.hidden = true; }
  addEventListener('pointerdown', (e) => { if (!menu.contains(e.target)) cerrarMenu(); }, true);
  addEventListener('keydown', (e) => { if (e.key === 'Escape') cerrarMenu(); });

  container.addEventListener('contextmenu', (e) => {
    if (e.defaultPrevented) return;        // el motor de dibujos ya lo ha usado
    const precio = precioEn(e.clientY);
    if (precio === null) return;
    e.preventDefault();
    menu.innerHTML = '';
    const cabecera = document.createElement('div');
    cabecera.className = 'cab';
    cabecera.textContent = `Alerta en ${fmtPrice(precio)}`;
    menu.appendChild(cabecera);
    const opciones = [
      ['⇅ cualquier cruce', 'any'],
      ['▲ al alza', 'up'],
      ['▼ a la baja', 'down'],
    ];
    for (const [txt, dir] of opciones) {
      const b = document.createElement('button');
      b.textContent = txt;
      b.onclick = async () => { cerrarMenu(); await crear(precio, dir); abrirPanel(); };
      menu.appendChild(b);
    }
    menu.hidden = false;
    // Que no se salga de la pantalla.
    const w = menu.offsetWidth, h = menu.offsetHeight;
    menu.style.left = `${Math.min(e.clientX, innerWidth - w - 8)}px`;
    menu.style.top = `${Math.min(e.clientY, innerHeight - h - 8)}px`;
  });

  // ---------- panel ----------
  const lista = panelEl.querySelector('[data-k="lista"]');
  const estadoEl = panelEl.querySelector('[data-k="estado"]');

  function abrirPanel() { panelEl.hidden = false; pintarPanel(); refrescarEstado(); }
  panelEl.querySelector('[data-act="cerrar"]').onclick = () => { panelEl.hidden = true; };
  panelEl.querySelector('[data-act="probar"]').onclick = async () => {
    await api.probar();
    estadoEl.textContent = 'mensaje de prueba encolado; el motor lo manda en unos segundos';
    setTimeout(refrescarEstado, 4000);
  };

  function pintarPanel() {
    if (!lista) return;
    lista.innerHTML = '';
    if (!alertas.length) {
      const p = document.createElement('div');
      p.className = 'vacio';
      p.textContent = 'Sin alertas. Click derecho en el gráfico para crear una.';
      lista.appendChild(p);
      return;
    }
    for (const a of alertas) {
      const fila = document.createElement('div');
      fila.className = 'alerta' + (a.status !== 'armed' ? ' apagada' : '');
      fila.dataset.id = a.id;

      const nivel = document.createElement('b');
      nivel.textContent = fmtPrice(a.level);
      fila.appendChild(nivel);

      const dir = document.createElement('select');
      for (const [v, t] of [['any', '⇅'], ['up', '▲'], ['down', '▼']]) {
        const o = document.createElement('option');
        o.value = v; o.textContent = t; o.selected = a.direction === v;
        dir.appendChild(o);
      }
      dir.onchange = async () => { a.direction = dir.value; await api.guardar(a); recargar(); };
      fila.appendChild(dir);

      const modo = document.createElement('select');
      for (const [v, t] of [['once', 'una vez'], ['recurring', 'recurrente']]) {
        const o = document.createElement('option');
        o.value = v; o.textContent = t; o.selected = a.mode === v;
        modo.appendChild(o);
      }
      modo.onchange = async () => { a.mode = modo.value; await api.guardar(a); recargar(); };
      fila.appendChild(modo);

      const nota = document.createElement('input');
      nota.type = 'text'; nota.placeholder = 'nota'; nota.value = a.note || '';
      nota.onchange = async () => { a.note = nota.value; await api.guardar(a); recargar(); };
      fila.appendChild(nota);

      const estado = document.createElement('span');
      estado.className = 'est';
      estado.textContent = a.status === 'armed' ? 'armada'
        : a.status === 'done' ? 'disparada' : 'pausada';
      fila.appendChild(estado);

      const rearmar = document.createElement('button');
      rearmar.textContent = '↻';
      rearmar.title = 'Volver a armar';
      rearmar.onclick = async () => { a.status = 'armed'; await api.guardar(a); recargar(); };
      fila.appendChild(rearmar);

      const borrar = document.createElement('button');
      borrar.textContent = '🗑';
      borrar.title = 'Borrar';
      borrar.onclick = async () => { await api.borrar(a.id); recargar(); };
      fila.appendChild(borrar);

      lista.appendChild(fila);
    }
  }

  async function refrescarEstado() {
    const s = await api.estado();
    if (!estadoEl) return;
    const partes = [];
    if (s.armadas !== undefined) partes.push(`${s.armadas} armada(s)`);
    if (s.retraso_seg !== undefined) partes.push(`motor al día hace ${s.retraso_seg}s`);
    if (s.motor && s.motor.telegram === false) partes.push('⚠ Telegram sin configurar');
    if (s.pendientes) partes.push(`${s.pendientes} sin enviar`);
    estadoEl.textContent = partes.join(' · ') || 'sin datos del motor';
  }

  recargar();
  setInterval(recargar, 30000);   // el motor cambia el estado por su cuenta

  return { recargar, crear, abrirPanel, alertas: () => alertas, api };
}
