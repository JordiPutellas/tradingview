// Spike F1c: 86.400 velas de 1s reales en LWC v5 + plugin de dibujo.
// Mide FPS de pan/zoom y ejercita crear/arrastrar/persistir una horizontal.
import { createChart, CandlestickSeries } from 'lightweight-charts';
import { DrawingManager, HorizontalLine } from 'lightweight-charts-drawing';

const container = document.getElementById('chart');
const chart = createChart(container, {
  width: 1280, height: 720,
  layout: { attributionLogo: true, background: { color: '#0e1117' }, textColor: '#c9d1d9' },
  timeScale: { timeVisible: true, secondsVisible: true },
});
const series = chart.addSeries(CandlestickSeries);

const dm = new DrawingManager();
dm.attach(chart, series, container);

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

// --- dibujo: crear, arrastrar (eventos sintéticos), persistir ---

window.runDrawing = () => {
  // Ancla en un (time, price) VISIBLE: el drag del DrawingManager exige
  // mousedown sobre el ancla (hitTestAnchor), no sobre cualquier punto.
  const ts = chart.timeScale();
  const range = ts.getVisibleRange();
  const time = Math.round((range.from + range.to) / 2);
  const midY = 360;
  const price = series.coordinateToPrice(midY);
  const hl = HorizontalLine.create('spike-hl', price, time, { color: '#f39c12', lineWidth: 2 }, { extendLeft: true, extendRight: true });
  dm.addDrawing(hl);
  dm.selectDrawing('spike-hl');

  const x0 = ts.timeToCoordinate(time);
  const y0 = series.priceToCoordinate(price);
  const rect = container.getBoundingClientRect();
  const fire = (type, x, y) => container.dispatchEvent(new MouseEvent(type, {
    bubbles: true, clientX: rect.left + x, clientY: rect.top + y, button: 0,
  }));
  fire('mousedown', x0, y0);
  for (let i = 1; i <= 10; i++) fire('mousemove', x0, y0 - 8 * i);
  fire('mouseup', x0, y0 - 80);

  const moved = dm.getDrawing('spike-hl');
  const newPrice = moved.anchors[0].price;

  // Persistencia: export → clear → import con factory.
  const json = JSON.stringify(dm.exportDrawings());
  dm.clearAll();
  const cleared = dm.getAllDrawings().length;
  dm.importDrawings(JSON.parse(json), (type, data) => {
    if (type === 'horizontal-line') {
      const d = new HorizontalLine(data.id);
      d.fromJSON(data);
      return d;
    }
    return null;
  });
  const restored = dm.getDrawing('spike-hl');
  return {
    createdAtPrice: price,
    priceAfterDrag: newPrice,
    dragWorked: Math.abs(newPrice - price) > 1,
    clearedCount: cleared,
    restoredPrice: restored ? restored.anchors[0].price : null,
    persistWorked: !!restored && Math.abs(restored.anchors[0].price - newPrice) < 1e-9,
  };
};
