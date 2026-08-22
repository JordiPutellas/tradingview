// Conversión tiempo UTC <-> índice lógico de LWC.
//
// La comparte el motor de dibujos (que necesita colocar figuras ENTRE velas y
// a la derecha de la última) y la navegación (que restaura un rango temporal
// al cambiar de timeframe, cuando las velas del destino aún no están
// cargadas). Fuera del rango de datos se EXTRAPOLA con el paso del timeframe:
// sin eso no hay forma de expresar "el instante X" como posición del gráfico.
//
// OJO con hacerlo con la API de LWC directamente: logicalToCoordinate(l)
// devuelve 0 si l no es entero y coordinateToLogical(x) redondea a la vela más
// cercana (trampa 14 del README).

// bars: filas [t,o,h,l,c,v] ascendentes. step: segundos por vela.
export function logicalOf(bars, step, time) {
  const n = bars.length;
  if (!n) return null;
  if (time <= bars[0][0]) return -((bars[0][0] - time) / step);
  if (time >= bars[n - 1][0]) return (n - 1) + (time - bars[n - 1][0]) / step;
  let lo = 0, hi = n - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (bars[mid][0] <= time) lo = mid; else hi = mid;
  }
  const span = (bars[hi][0] - bars[lo][0]) || step;
  return lo + (time - bars[lo][0]) / span;
}

export function timeOfLogical(bars, step, l) {
  const n = bars.length;
  if (!n) return null;
  if (l <= 0) return Math.round(bars[0][0] + l * step);
  if (l >= n - 1) return Math.round(bars[n - 1][0] + (l - (n - 1)) * step);
  const i = Math.floor(l), f = l - i;
  return Math.round(bars[i][0] + f * ((bars[i + 1][0] - bars[i][0]) || step));
}
