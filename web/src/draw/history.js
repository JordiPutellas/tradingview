// Pila de deshacer / rehacer (F4-3.1).
//
// Guarda ESTADOS completos (el JSON de todas las figuras), no operaciones
// inversas. Con unas decenas de figuras el coste es despreciable y a cambio no
// hay forma de que un "deshacer" reconstruya mal una operación compuesta:
// mover una zona toca dos puntos, redimensionar un rectángulo cambia ejes
// distintos de dos puntos, y aplicar una plantilla cambia dos estilos a la vez.
//
// `base` es el estado ya confirmado. commit() apila el anterior; deshacer y
// rehacer mueven `base` entre las dos pilas.
export class History {
  constructor(limite = 100) {
    this.limite = limite;
    this.base = null;
    this.atras = [];
    this.adelante = [];
  }

  init(estado) { this.base = estado; this.atras = []; this.adelante = []; }

  commit(estado) {
    if (estado === this.base) return false;      // nada cambió: no ensucia la pila
    if (this.base !== null) this.atras.push(this.base);
    if (this.atras.length > this.limite) this.atras.shift();
    this.base = estado;
    this.adelante = [];                          // rehacer muere al hacer algo nuevo
    return true;
  }

  undo() {
    if (!this.atras.length) return null;
    this.adelante.push(this.base);
    this.base = this.atras.pop();
    return this.base;
  }

  redo() {
    if (!this.adelante.length) return null;
    this.atras.push(this.base);
    this.base = this.adelante.pop();
    return this.base;
  }

  get puedeDeshacer() { return this.atras.length > 0; }
  get puedeRehacer() { return this.adelante.length > 0; }
}
