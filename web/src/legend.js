// Legend OHLC (F4-3.2): open, high, low, close y variación de la vela bajo el
// cursor, arriba a la izquierda del gráfico.
//
// La variación es contra el CIERRE ANTERIOR (como en TradingView), no contra
// la apertura de la propia vela: es la que dice cuánto se ha movido el precio
// desde que se miró por última vez. En la primera vela cargada, que no tiene
// anterior, se usa su apertura.
//
// Los colores son los del usuario (velas alcistas y bajistas), no el
// verde/rojo de serie.
import { fmtPrice } from './draw/geom.js';

export function mountLegend({ chart, series, el, getBars, up, down }) {
  let encima = false;

  const buscar = (t) => {
    const b = getBars();
    let lo = 0, hi = b.length - 1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (b[mid][0] === t) return mid;
      if (b[mid][0] < t) lo = mid + 1; else hi = mid - 1;
    }
    return -1;
  };

  function pinta(vela, previo) {
    if (!vela) { el.textContent = ''; return; }
    const [, o, h, l, c] = vela;
    const ref = Number.isFinite(previo) ? previo : o;
    const d = c - ref, pct = ref ? (d / ref) * 100 : 0;
    const color = d >= 0 ? up : down;
    const signo = d >= 0 ? '+' : '−';
    el.innerHTML =
      `O <b>${fmtPrice(o)}</b>  H <b>${fmtPrice(h)}</b>  L <b>${fmtPrice(l)}</b>  C <b>${fmtPrice(c)}</b>`
      + `  <span class="var" style="color:${color}">${signo}${fmtPrice(Math.abs(d))}`
      + ` (${signo}${Math.abs(pct).toLocaleString('es-ES', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}%)</span>`;
  }

  function ultima() {
    const b = getBars();
    if (!b.length) { el.textContent = ''; return; }
    pinta(b[b.length - 1], b.length > 1 ? b[b.length - 2][4] : undefined);
  }

  chart.subscribeCrosshairMove((p) => {
    const d = p.seriesData && p.seriesData.get(series);
    if (!d || !p.point) { encima = false; ultima(); return; }
    encima = true;
    const i = buscar(p.time);
    const vela = i >= 0 ? getBars()[i] : [p.time, d.open, d.high, d.low, d.close, 0];
    pinta(vela, i > 0 ? getBars()[i - 1][4] : undefined);
  });

  ultima();
  // refresh(): al llegar un tick, si el ratón no está encima, la legend sigue
  // al precio en vivo.
  return { refresh: () => { if (!encima) ultima(); }, get encima() { return encima; } };
}
