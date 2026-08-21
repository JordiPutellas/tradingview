// Spike F1c: 86.400 velas de 1s reales en LWC v5 + plugin de dibujo.
// Mide FPS de pan/zoom y ejercita crear/arrastrar/persistir una horizontal.
import { createChart, CandlestickSeries } from 'lightweight-charts';

const container = document.getElementById('chart');
const chart = createChart(container, {
  width: 1280, height: 720,
  layout: { attributionLogo: true, background: { color: '#0e1117' }, textColor: '#c9d1d9' },
  timeScale: { timeVisible: true, secondsVisible: true },
});
const series = chart.addSeries(CandlestickSeries);


window.__ready = (async () => {
  // Día completo de 1s desde la API real (2026-08-19, día activo de F0),
  // paginado con el mismo mecanismo que usará el lazy-loading (cap 20k).
  const from = Date.UTC(2026, 7, 19) / 1000, to = from + 86400;
  const t0 = performance.now();
  const rows = [];
  let cursor = from;
  while (cursor < to) {
    const resp = await fetch(`/api/candles?tf=1s&from=${cursor}&to=${to}&limit=20000`);
    const page = await resp.json();
    if (!page.length) break;
    rows.push(...page);
    cursor = page[page.length - 1][0] + 1;
  }
  const fetchMs = performance.now() - t0;
  const t1 = performance.now();
  series.setData(rows.map(([t, o, h, l, c]) => ({ time: t, open: o, high: h, low: l, close: c })));
  const setDataMs = performance.now() - t1;
  chart.timeScale().fitContent();
  return { bars: rows.length, fetchMs: Math.round(fetchMs), setDataMs: Math.round(setDataMs) };
})();

function measureFPS(mutate, ms = 4000) {
  return new Promise(resolve => {
    let frames = 0;
    const start = performance.now();
    function step() {
      frames++;
      mutate(performance.now() - start);
      if (performance.now() - start < ms) requestAnimationFrame(step);
      else resolve(Math.round(frames / ((performance.now() - start) / 1000)));
    }
    requestAnimationFrame(step);
  });
}

window.runPan = () => {
  const ts = chart.timeScale();
  ts.setVisibleLogicalRange({ from: 80000, to: 86400 });
  const width = 6400;
  return measureFPS(elapsed => {
    const from = 80000 - elapsed * 15; // ~15 barras/ms de desplazamiento
    ts.setVisibleLogicalRange({ from, to: from + width });
  });
};

window.runZoom = () => {
  const ts = chart.timeScale();
  return measureFPS(elapsed => {
    const width = 2000 + 40000 * (0.5 + 0.5 * Math.sin(elapsed / 300));
    const center = 43200;
    ts.setVisibleLogicalRange({ from: center - width / 2, to: center + width / 2 });
  });
};

// --- dibujo ---
// El spike de F1c validaba aquí el plugin lightweight-charts-drawing. En F3 el
// plugin se retiró (motor propio en src/draw/) y esta parte del spike se queda
// sin objeto: la verificación de crear/arrastrar/persistir vive ahora en
// app-test.mjs con gestos reales sobre la aplicación de verdad.
window.runDrawing = () => ({ retirado: 'ver app-test.mjs (F3)' });
