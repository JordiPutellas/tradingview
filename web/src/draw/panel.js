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
  };
  const rows = { fill: q('.row-fill'), text: q('.row-text'), zone: q('.row-zone') };

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
    panelEl.querySelectorAll('[data-line]').forEach(b => {
      b.classList.toggle('active', Number(b.dataset.line) === engine.activeLine);
    });
    place(s);
  }

  // Se coloca junto a la figura y se recoloca en cada repintado (al desplazar
  // o hacer zoom el dibujo se mueve y el panel debe seguirlo).
  function place(s) {
    const pts = engine.screenPoints(s);
    const r = engine.paneRect();
    if (!pts || !r) return;
    const x = Math.max(...pts.map(p => p.x)), y = Math.min(...pts.map(p => p.y));
    const w = panelEl.offsetWidth || 240, h = panelEl.offsetHeight || 120;
    const left = Math.min(Math.max(8, r.left + x + 12), innerWidth - w - 8);
    const top = Math.min(Math.max(8, r.top + y + 12), innerHeight - h - 8);
    panelEl.style.left = `${left}px`;
    panelEl.style.top = `${top}px`;
  }

  const prevOnSelect = engine.onSelect;
  engine.onSelect = (s, e) => { if (prevOnSelect) prevOnSelect(s, e); refresh(s); };
  engine.onRender = () => { const s = engine.selected(); if (s && !panelEl.hidden) place(s); };
  return { refresh };
}
