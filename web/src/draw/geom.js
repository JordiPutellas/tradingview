// Geometría de hit-testing y formateo. Todo en coordenadas de PANTALLA (px
// CSS del panel del gráfico): la conversión desde (tiempo, precio) la hace
// engine.js, así que aquí no se sabe nada de velas.

export const HANDLE = 4;        // medio lado del cuadradito de un punto de control
export const TOL = 6;           // tolerancia de acierto sobre una línea, en px

export function dist(x1, y1, x2, y2) {
  return Math.hypot(x2 - x1, y2 - y1);
}

// Distancia de un punto al SEGMENTO ab (no a la recta infinita).
export function distToSegment(px, py, ax, ay, bx, by) {
  const vx = bx - ax, vy = by - ay;
  const len2 = vx * vx + vy * vy;
  if (len2 === 0) return dist(px, py, ax, ay);   // segmento degenerado
  let t = ((px - ax) * vx + (py - ay) * vy) / len2;
  t = Math.max(0, Math.min(1, t));
  return dist(px, py, ax + t * vx, ay + t * vy);
}

// Distancia al borde de un rectángulo definido por dos esquinas opuestas.
export function distToRectBorder(px, py, x1, y1, x2, y2) {
  const l = Math.min(x1, x2), r = Math.max(x1, x2);
  const t = Math.min(y1, y2), b = Math.max(y1, y2);
  return Math.min(
    distToSegment(px, py, l, t, r, t), distToSegment(px, py, r, t, r, b),
    distToSegment(px, py, r, b, l, b), distToSegment(px, py, l, b, l, t));
}

export function insideRect(px, py, x1, y1, x2, y2) {
  return px >= Math.min(x1, x2) && px <= Math.max(x1, x2)
      && py >= Math.min(y1, y2) && py <= Math.max(y1, y2);
}

// Bézier cúbica por 4 puntos de control, muestreada. Se usa tanto para pintar
// (con bezierCurveTo basta) como para el hit-test, que necesita puntos.
export function sampleBezier(p, n = 40) {
  const out = [];
  for (let i = 0; i <= n; i++) {
    const t = i / n, u = 1 - t;
    out.push({
      x: u*u*u*p[0].x + 3*u*u*t*p[1].x + 3*u*t*t*p[2].x + t*t*t*p[3].x,
      y: u*u*u*p[0].y + 3*u*u*t*p[1].y + 3*u*t*t*p[2].y + t*t*t*p[3].y,
    });
  }
  return out;
}

// Circunferencia que pasa por tres puntos: centro por intersección de las
// mediatrices. Devuelve null si son colineales (denominador ~0), y el llamante
// dibuja entonces una recta.
export function circleThrough(a, b, c) {
  const d = 2 * (a.x * (b.y - c.y) + b.x * (c.y - a.y) + c.x * (a.y - b.y));
  if (Math.abs(d) < 1e-6) return null;
  const a2 = a.x * a.x + a.y * a.y, b2 = b.x * b.x + b.y * b.y, c2 = c.x * c.x + c.y * c.y;
  const cx = (a2 * (b.y - c.y) + b2 * (c.y - a.y) + c2 * (a.y - b.y)) / d;
  const cy = (a2 * (c.x - b.x) + b2 * (a.x - c.x) + c2 * (b.x - a.x)) / d;
  return { cx, cy, r: dist(cx, cy, a.x, a.y) };
}

// Arco a→c pasando por b: ángulos y sentido de giro para ctx.arc().
export function arcThrough(a, b, c) {
  const circ = circleThrough(a, b, c);
  if (!circ) return null;
  const ang = (p) => Math.atan2(p.y - circ.cy, p.x - circ.cx);
  const a0 = ang(a), a1 = ang(b), a2 = ang(c);
  // sentido: el que hace que b caiga ENTRE a y c
  const norm = (x) => (x % (2 * Math.PI) + 2 * Math.PI) % (2 * Math.PI);
  const cw = norm(a1 - a0) < norm(a2 - a0);
  return { ...circ, from: a0, to: a2, ccw: !cw };
}

export function distToArc(px, py, arc) {
  const d = dist(px, py, arc.cx, arc.cy);
  const ang = Math.atan2(py - arc.cy, px - arc.cx);
  const norm = (x) => (x % (2 * Math.PI) + 2 * Math.PI) % (2 * Math.PI);
  const span = arc.ccw ? norm(arc.from - arc.to) : norm(arc.to - arc.from);
  const rel = arc.ccw ? norm(arc.from - ang) : norm(ang - arc.from);
  if (rel > span) {  // fuera del tramo: distancia al extremo más cercano
    const e1 = { x: arc.cx + arc.r * Math.cos(arc.from), y: arc.cy + arc.r * Math.sin(arc.from) };
    const e2 = { x: arc.cx + arc.r * Math.cos(arc.to), y: arc.cy + arc.r * Math.sin(arc.to) };
    return Math.min(dist(px, py, e1.x, e1.y), dist(px, py, e2.x, e2.y));
  }
  return Math.abs(d - arc.r);
}

export function distToPolyline(px, py, pts) {
  let best = Infinity;
  for (let i = 1; i < pts.length; i++) {
    best = Math.min(best, distToSegment(px, py, pts[i-1].x, pts[i-1].y, pts[i].x, pts[i].y));
  }
  return best;
}

// #rrggbb + alfa -> rgba(). Acepta ya-rgba y lo devuelve tal cual.
export function rgba(hex, alpha = 1) {
  if (!hex) return 'rgba(0,0,0,0)';
  if (hex.startsWith('rgb')) return hex;
  const h = hex.replace('#', '');
  const n = parseInt(h.length === 3 ? h.split('').map(c => c + c).join('') : h, 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${alpha})`;
}

// "10d 20h": las dos unidades más significativas, sin ceros a la izquierda.
export function fmtDuration(seconds) {
  const s = Math.abs(Math.round(seconds));
  if (s < 60) return `${s}s`;
  const u = [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]];
  const parts = [];
  let rest = s;
  for (const [name, size] of u) {
    const v = Math.floor(rest / size);
    rest -= v * size;
    if (v > 0 || parts.length) parts.push(`${v}${name}`);
    if (parts.length === 2) break;
  }
  return parts.filter(p => !/^0/.test(p) || parts.indexOf(p) === 0).slice(0, 2).join(' ');
}

// Precio con los decimales justos para BTC (2) y separador de miles.
export function fmtPrice(p) {
  if (!Number.isFinite(p)) return '—';
  return p.toLocaleString('es-ES', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
