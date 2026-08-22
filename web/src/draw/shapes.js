// Catálogo de figuras. Cada tipo declara cuántos puntos necesita, cómo se
// pinta, cómo se acierta con el ratón y dónde van sus puntos de control.
//
// Todo trabaja en coordenadas de pantalla ya convertidas (pts = [{x,y}]);
// el modelo guarda (tiempo, precio) y de la conversión se encarga engine.js.
import {
  TOL, HANDLE, distToSegment, distToRectBorder, insideRect, dist,
  sampleBezier, distToPolyline, arcThrough, distToArc, rgba,
} from './geom.js';

export const DEFAULT_STYLE = {
  color: '#f0b90b',
  width: 2,
  opacity: 1,
  fill: '#f0b90b',
  fillOpacity: 0.12,
  dash: false,
};

function stroke(ctx, s, style = s.style) {
  ctx.strokeStyle = rgba(style.color, style.opacity);
  ctx.lineWidth = style.width;
  ctx.setLineDash(style.dash ? [6, 4] : []);
  ctx.lineCap = 'butt';
}

const fillOf = (s) => rgba(s.style.fill, s.style.fillOpacity);

const line = (ctx, x1, y1, x2, y2) => {
  ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2); ctx.stroke();
};

export const TYPES = {
  // --- horizontales ---
  hline: {
    label: 'Línea horizontal', points: 1, fill: false, priceLabel: true,
    draw(ctx, s, p, env) { stroke(ctx, s); line(ctx, 0, p[0].y, env.width, p[0].y); },
    hit: (x, y, s, p) => Math.abs(y - p[0].y) <= TOL,
    handles: (p) => [p[0]],
  },
  hray: {
    label: 'Horizontal a la derecha', points: 1, fill: false, priceLabel: true,
    draw(ctx, s, p, env) { stroke(ctx, s); line(ctx, p[0].x, p[0].y, env.width, p[0].y); },
    hit: (x, y, s, p) => Math.abs(y - p[0].y) <= TOL && x >= p[0].x - TOL,
    handles: (p) => [p[0]],
  },
  // --- zona de dos niveles: dos horizontales vinculadas ---
  zone2: {
    label: 'Zona de dos niveles', points: 2, fill: true, priceLabel: true, linked: true,
    draw(ctx, s, p, env) {
      ctx.fillStyle = fillOf(s);
      ctx.fillRect(p[0].x, Math.min(p[0].y, p[1].y), env.width - p[0].x, Math.abs(p[1].y - p[0].y));
      stroke(ctx, s, s.style);  line(ctx, p[0].x, p[0].y, env.width, p[0].y);
      stroke(ctx, s, s.style2 || s.style); line(ctx, p[1].x, p[1].y, env.width, p[1].y);
    },
    hit(x, y, s, p) {
      if (x < Math.min(p[0].x, p[1].x) - TOL) return false;
      return Math.abs(y - p[0].y) <= TOL || Math.abs(y - p[1].y) <= TOL
        || (y > Math.min(p[0].y, p[1].y) && y < Math.max(p[0].y, p[1].y));
    },
    handles: (p) => [p[0], p[1]],
  },
  // --- trazos ---
  trend: {
    label: 'Línea de tendencia', points: 2, fill: false,
    draw(ctx, s, p) { stroke(ctx, s); line(ctx, p[0].x, p[0].y, p[1].x, p[1].y); },
    hit: (x, y, s, p) => distToSegment(x, y, p[0].x, p[0].y, p[1].x, p[1].y) <= TOL,
    handles: (p) => p,
  },
  rect: {
    label: 'Rectángulo', points: 2, fill: true,
    draw(ctx, s, p) {
      const x = Math.min(p[0].x, p[1].x), y = Math.min(p[0].y, p[1].y);
      const w = Math.abs(p[1].x - p[0].x), h = Math.abs(p[1].y - p[0].y);
      ctx.fillStyle = fillOf(s); ctx.fillRect(x, y, w, h);
      stroke(ctx, s); ctx.strokeRect(x, y, w, h);
    },
    hit: (x, y, s, p) => distToRectBorder(x, y, p[0].x, p[0].y, p[1].x, p[1].y) <= TOL
      || insideRect(x, y, p[0].x, p[0].y, p[1].x, p[1].y),
    // Cuatro esquinas (círculos) y cuatro centros de lado (cuadrados), como
    // en TradingView: la esquina mueve los dos ejes y el lado mueve SOLO el
    // suyo, así se ajusta el alto o el ancho sin tocar el otro anclaje.
    handles: (p) => {
      const mx = (p[0].x + p[1].x) / 2, my = (p[0].y + p[1].y) / 2;
      return [
        { x: p[0].x, y: p[0].y, forma: 'circulo' },
        { x: p[1].x, y: p[0].y, forma: 'circulo' },
        { x: p[1].x, y: p[1].y, forma: 'circulo' },
        { x: p[0].x, y: p[1].y, forma: 'circulo' },
        { x: mx, y: p[0].y },      // lado de arriba
        { x: p[1].x, y: my },      // lado derecho
        { x: mx, y: p[1].y },      // lado de abajo
        { x: p[0].x, y: my },      // lado izquierdo
      ];
    },
    // El rectángulo tiene 8 tiradores pero solo 2 puntos: cada uno toca una
    // combinación distinta de ejes.
    handleTargets: (i) => [
      [{ i: 0, axes: 'xy' }],
      [{ i: 1, axes: 'x' }, { i: 0, axes: 'y' }],
      [{ i: 1, axes: 'xy' }],
      [{ i: 0, axes: 'x' }, { i: 1, axes: 'y' }],
      [{ i: 0, axes: 'y' }],
      [{ i: 1, axes: 'x' }],
      [{ i: 1, axes: 'y' }],
      [{ i: 0, axes: 'x' }],
    ][i] || [],
  },
  curve: {
    label: 'Curva', points: 4, fill: false,
    draw(ctx, s, p) {
      stroke(ctx, s);
      ctx.beginPath(); ctx.moveTo(p[0].x, p[0].y);
      ctx.bezierCurveTo(p[1].x, p[1].y, p[2].x, p[2].y, p[3].x, p[3].y);
      ctx.stroke();
    },
    hit: (x, y, s, p) => distToPolyline(x, y, sampleBezier(p)) <= TOL,
    handles: (p) => p,
  },
  arc: {
    label: 'Arco', points: 3, fill: false,
    draw(ctx, s, p) {
      stroke(ctx, s);
      const a = arcThrough(p[0], p[1], p[2]);
      ctx.beginPath();
      if (!a) { ctx.moveTo(p[0].x, p[0].y); ctx.lineTo(p[2].x, p[2].y); }  // colineales
      else ctx.arc(a.cx, a.cy, a.r, a.from, a.to, a.ccw);
      ctx.stroke();
    },
    hit(x, y, s, p) {
      const a = arcThrough(p[0], p[1], p[2]);
      if (!a) return distToSegment(x, y, p[0].x, p[0].y, p[2].x, p[2].y) <= TOL;
      return distToArc(x, y, a) <= TOL;
    },
    handles: (p) => p,
  },
  text: {
    label: 'Texto', points: 1, fill: true, text: true,
    draw(ctx, s, p) {
      const t = s.text || '';
      ctx.font = `${10 + 2 * s.style.width}px system-ui, sans-serif`;
      ctx.textBaseline = 'middle';
      const w = ctx.measureText(t).width, h = 12 + 2 * s.style.width;
      ctx.fillStyle = fillOf(s);
      ctx.fillRect(p[0].x - 3, p[0].y - h / 2 - 2, w + 6, h + 4);
      ctx.fillStyle = rgba(s.style.color, s.style.opacity);
      ctx.fillText(t, p[0].x, p[0].y);
    },
    hit(x, y, s, p, env) {
      const w = env.measure(s.text || '', 10 + 2 * s.style.width), h = 16 + 2 * s.style.width;
      return insideRect(x, y, p[0].x - 4, p[0].y - h / 2, p[0].x + w + 4, p[0].y + h / 2);
    },
    handles: (p) => [p[0]],
  },
};

// Puntos de control: cuadraditos, SOLO con la figura seleccionada (el usuario
// no quiere handles permanentes).
export function drawHandles(ctx, pts) {
  ctx.setLineDash([]);
  ctx.lineWidth = 1;
  for (const p of pts) {
    ctx.fillStyle = '#ffffff';
    ctx.strokeStyle = '#111111';
    if (p.forma === 'circulo') {       // anclas del rectángulo
      ctx.beginPath();
      ctx.arc(p.x, p.y, HANDLE + 1, 0, 2 * Math.PI);
      ctx.fill();
      ctx.stroke();
    } else {                            // centros de lado
      ctx.fillRect(p.x - HANDLE, p.y - HANDLE, HANDLE * 2, HANDLE * 2);
      ctx.strokeRect(p.x - HANDLE, p.y - HANDLE, HANDLE * 2, HANDLE * 2);
    }
  }
}

// Realce de selección: la misma figura repintada debajo con un halo claro.
export function drawSelection(ctx, s, pts, env) {
  const halo = {
    ...s,
    style: { ...s.style, color: '#ffffff', opacity: 0.35, width: s.style.width + 4, dash: false },
    style2: s.style2 ? { ...s.style2, color: '#ffffff', opacity: 0.35, width: s.style2.width + 4, dash: false } : undefined,
  };
  ctx.save();
  TYPES[s.type].draw(ctx, halo, pts, env);
  ctx.restore();
}
