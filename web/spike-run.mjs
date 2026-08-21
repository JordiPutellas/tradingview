// Runner headless del spike F1c. Uso: node spike-run.mjs
// Requiere la API en 127.0.0.1:8090 sirviendo web/dist.
// OJO: FPS en headless = rasterizado por software → suelo conservador; en un
// navegador real con GPU será mayor.
import { chromium } from 'playwright';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
page.on('pageerror', e => console.error('PAGE ERROR:', e.message));

await page.goto('http://127.0.0.1:8090/spike.html');
const load = await page.evaluate(() => window.__ready);
console.log('CARGA:', JSON.stringify(load));

const panFPS = await page.evaluate(() => window.runPan());
console.log('PAN FPS:', panFPS);
const zoomFPS = await page.evaluate(() => window.runZoom());
console.log('ZOOM FPS:', zoomFPS);

const drawing = await page.evaluate(() => window.runDrawing());
console.log('DIBUJO:', JSON.stringify(drawing, null, 1));

await browser.close();
