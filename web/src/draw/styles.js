// Estilos por defecto y plantillas con nombre (F4-2.1 y F4-2.2).
//
// Son el MISMO sistema: el "estilo actual" de cada herramienta no es más que
// una plantilla implícita. Tocar cualquier control del panel la reescribe, y
// una plantilla con nombre es una copia guardada de eso mismo.
//
// Precedencia al crear una figura:
//   1. lo último que se aplicó a ese tipo de figura       (2.1)
//   2. la plantilla marcada como predeterminada           (2.2)
//   3. DEFAULT_STYLE
// Marcar una plantilla como predeterminada BORRA la memoria por tipo: es una
// orden de "a partir de ahora, todo lo nuevo así", y si no, no se notaría en
// las herramientas ya tocadas.
//
// Vive en localStorage y no en la base de datos: es preferencia del navegador,
// como la posición de la barra de herramientas. Los dibujos, que sí son datos,
// siguen guardándose en el servidor.
import { DEFAULT_STYLE } from './shapes.js';

const K_CUR = 'btcdash.estiloActual';
const K_TPL = 'btcdash.plantillas';
const K_DEF = 'btcdash.plantillaDefecto';

const leer = (k, def) => {
  try { const v = JSON.parse(localStorage.getItem(k)); return v && typeof v === 'object' ? v : def; }
  catch { return def; }                      // guardado corrupto: se ignora
};
const escribir = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* cuota */ } };
const completo = (s) => ({ ...DEFAULT_STYLE, ...(s || {}) });

export class Estilos {
  constructor() {
    this.cur = leer(K_CUR, {});
    this.tpl = leer(K_TPL, {});
    this.def = localStorage.getItem(K_DEF) || null;
  }

  // Estilo con el que nace una figura nueva de ese tipo.
  para(type) {
    const base = this.cur[type] || (this.def && this.tpl[this.def]) || {};
    return {
      style: completo(base.style),
      style2: completo(base.style2 || { ...base.style, color: '#3fb950' }),
    };
  }

  recordar(type, style, style2) {
    this.cur[type] = { style: { ...style }, ...(style2 ? { style2: { ...style2 } } : {}) };
    escribir(K_CUR, this.cur);
  }

  nombres() { return Object.keys(this.tpl).sort((a, b) => a.localeCompare(b, 'es')); }
  get(nombre) { return this.tpl[nombre] || null; }

  guardar(nombre, style, style2) {
    const n = String(nombre || '').trim().slice(0, 40);
    if (!n) return null;
    this.tpl[n] = { style: { ...style }, ...(style2 ? { style2: { ...style2 } } : {}) };
    escribir(K_TPL, this.tpl);
    return n;
  }

  borrar(nombre) {
    if (!this.tpl[nombre]) return;
    delete this.tpl[nombre];
    escribir(K_TPL, this.tpl);
    if (this.def === nombre) { this.def = null; localStorage.removeItem(K_DEF); }
  }

  marcarDefecto(nombre) {
    if (!this.tpl[nombre]) return;
    this.def = nombre;
    localStorage.setItem(K_DEF, nombre);
    this.cur = {};                 // que mande la plantilla también en lo ya tocado
    escribir(K_CUR, this.cur);
  }
}
