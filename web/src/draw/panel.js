// Panel de configuración del dibujo seleccionado: flotante, junto a la figura,
// con cambios en vivo (cada input aplica y persiste con el debounce del motor).
import { TYPES } from './shapes.js';

export function mountPanel(engine, panelEl) {
  const q = (sel) => panelEl.querySelector(sel);
  const inputs = {
    color: q('[data-k="color"]'),
    width: q('[data-k="width"]'),
    dash: q('[data-k="dash"]'),
    opacity: q('[data-k="opacity"]'),
    fill: q('[data-k="fill"]'),
    fillOpacity: q('[data-k="fillOpacity"]'),
    text: q('[data-k="text"]'),
    tpl: q('[data-k="tpl"]'),
    tplName: q('[data-k="tplName"]'),
  };
  const rows = { fill: q('.row-fill'), text: q('.row-text'), zone: q('.row-zone'),
    tplName: q('.row-tpl-name') };

  // El panel vive fuera del contenedor del gráfico: sus clicks no llegan al
  // motor y por tanto no deseleccionan.
  panelEl.addEventListener('pointerdown', (e) => e.stopPropagation());

  const patch = (k, v) => engine.patchStyle({ [k]: v });
  inputs.color.oninput = (e) => patch('color', e.target.value);
  inputs.fill.oninput = (e) => patch('fill', e.target.value);
  inputs.width.oninput = (e) => patch('width', Number(e.target.value));
  inputs.dash.oninput = (e) => patch('dash', e.target.value === '1');
  inputs.opacity.oninput = (e) => patch('opacity', Number(e.target.value));
  inputs.fillOpacity.oninput = (e) => patch('fillOpacity', Number(e.target.value));
  inputs.text.oninput = (e) => engine.setText(e.target.value);
  q('[data-act="del"]').onclick = () => engine.deleteSelected();

  // --- plantillas de estilo (F4-2.2) ---
  // Elegir una la aplica a la figura seleccionada en el acto: el requisito es
  // poder cambiar el estilo de un dibujo YA hecho, no solo de los próximos.
  function pintarPlantillas(elegida) {
    const est = engine.estilos;
    const nombres = est.nombres();
    inputs.tpl.innerHTML = '';
    const vacio = document.createElement('option');
    vacio.value = ''; vacio.textContent = nombres.length ? '— plantilla —' : '— sin plantillas —';
    inputs.tpl.appendChild(vacio);
    for (const n of nombres) {
      const o = document.createElement('option');
      o.value = n;
      o.textContent = est.def === n ? `★ ${n}` : n;   // la predeterminada, marcada
      inputs.tpl.appendChild(o);
    }
    inputs.tpl.value = elegida && nombres.includes(elegida) ? elegida : '';
  }
  inputs.tpl.onchange = () => {
    if (inputs.tpl.value) engine.aplicarPlantilla(inputs.tpl.value);
  };
  q('[data-act="tpl-save"]').onclick = () => {
    rows.tplName.hidden = false;
    inputs.tplName.value = inputs.tpl.value || '';
    inputs.tplName.focus();
  };
  const guardarPlantilla = () => {
    const n = engine.guardarPlantilla(inputs.tplName.value);
    if (!n) return;
    rows.tplName.hidden = true;
    pintarPlantillas(n);
  };
  q('[data-act="tpl-ok"]').onclick = guardarPlantilla;
  inputs.tplName.onkeydown = (e) => {
    if (e.key === 'Enter') { e.preventDefault(); guardarPlantilla(); }
    if (e.key === 'Escape') { e.preventDefault(); rows.tplName.hidden = true; inputs.tplName.blur(); }
  };
  q('[data-act="tpl-def"]').onclick = () => {
    if (!inputs.tpl.value) return;
    engine.estilos.marcarDefecto(inputs.tpl.value);
    pintarPlantillas(inputs.tpl.value);
  };
  q('[data-act="tpl-del"]').onclick = () => {
    if (!inputs.tpl.value) return;
    engine.estilos.borrar(inputs.tpl.value);
    pintarPlantillas(null);
  };
  panelEl.querySelectorAll('[data-line]').forEach(b => {
    b.onclick = () => { engine.activeLine = Number(b.dataset.line); refresh(engine.selected()); };
  });

  function refresh(s) {
    if (!s) { panelEl.hidden = true; return; }
    const t = TYPES[s.type];
    const st = engine.styleOf(s);
    panelEl.hidden = false;
    inputs.color.value = st.color;
    inputs.width.value = String(st.width);
    inputs.dash.value = st.dash ? '1' : '0';
    inputs.opacity.value = String(st.opacity);
    inputs.fill.value = st.fill;
    inputs.fillOpacity.value = String(st.fillOpacity);
    rows.fill.hidden = !t.fill;
    rows.text.hidden = !t.text;
    rows.zone.hidden = !t.linked;
    if (t.text) inputs.text.value = s.text || '';
    rows.tplName.hidden = true;      // el cajetín del nombre no se queda abierto
    pintarPlantillas(inputs.tpl.value);
    panelEl.querySelectorAll('[data-line]').forEach(b => {
      b.classList.toggle('active', Number(b.dataset.line) === engine.activeLine);
    });
    place(s);
  }

  // Se coloca PEGADO a la figura pero sin taparla: si el panel cae encima,
  // el usuario ya no puede arrastrar el dibujo, porque los clicks se los queda
  // el panel (pasó con la zona de dos niveles y con el rectángulo). Se prueban
  // cuatro posiciones y se elige la primera que quepa y no solape.
  function place(s) {
    const pts = engine.screenPoints(s);
    const r = engine.paneRect();
    if (!pts || !r) return;
    const w = panelEl.offsetWidth || 230, h = panelEl.offsetHeight || 120;
    const M = 12;
    const caja = {
      x1: r.left + Math.min(...pts.map(p => p.x)), x2: r.left + Math.max(...pts.map(p => p.x)),
      y1: r.top + Math.min(...pts.map(p => p.y)), y2: r.top + Math.max(...pts.map(p => p.y)),
    };
    // Las figuras horizontales se pintan hasta el borde derecho del panel:
    // su caja real ocupa todo el ancho, así que solo caben arriba o abajo.
    const anchaHastaElBorde = ['hline', 'hray', 'zone2'].includes(s.type);
    if (anchaHastaElBorde) { caja.x1 = r.left; caja.x2 = r.right; }
    const cabe = (x, y) => x >= 8 && y >= 8 && x + w <= innerWidth - 8 && y + h <= innerHeight - 8;
    const solapa = (x, y) => x < caja.x2 + 4 && x + w > caja.x1 - 4 && y < caja.y2 + 4 && y + h > caja.y1 - 4;
    const opciones = [
      [caja.x1, caja.y2 + M],          // debajo
      [caja.x1, caja.y1 - h - M],      // encima
      [caja.x2 + M, caja.y1],          // a la derecha
      [caja.x1 - w - M, caja.y1],      // a la izquierda
    ];
    let elegida = opciones.find(([x, y]) => cabe(x, y) && !solapa(x, y))
      || opciones.find(([x, y]) => cabe(x, y))
      || [caja.x1, caja.y2 + M];
    const left = Math.min(Math.max(8, elegida[0]), innerWidth - w - 8);
    const top = Math.min(Math.max(8, elegida[1]), innerHeight - h - 8);
    panelEl.style.left = `${left}px`;
    panelEl.style.top = `${top}px`;
  }

  const prevOnSelect = engine.onSelect;
  engine.onSelect = (s, e) => { if (prevOnSelect) prevOnSelect(s, e); refresh(s); };
  engine.onRender = () => { const s = engine.selected(); if (s && !panelEl.hidden) place(s); };
  return { refresh };
}
