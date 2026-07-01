// Records the Mercator console demo (docs/assets/mercator-demo.webm) with
// Playwright. It seeds a run through the API if the workspace is empty, then
// walks the console with a synthetic cursor and Screen-Studio-style motion:
// the pointer glides (ghost-cursor paths) to each target, clicks leave a
// ripple, and the view eases-zooms into the placement decision and the event
// trail. Sequence: runs list -> run detail -> Decision -> Events.
//
// Prerequisites: a running Docker daemon, the broker up on $BASE (see the
// README "Try It In 5 Minutes" quickstart), and:
//
//   npm i playwright ghost-cursor && npx playwright install chromium
//   node docs/assets/record-demo.mjs
//   # then convert to the GIF fallback (see docs/assets/README.md) and move
//   # both files into docs/assets/.
//
// The token and workspace are seeded into localStorage (never the URL), exactly
// as the console itself stores them, so nothing sensitive is captured. The run
// is short-lived busybox `echo` — no private image, host path, or credential.

import { chromium } from "playwright";
import { path } from "ghost-cursor";
import { execSync } from "node:child_process";
import fs from "node:fs";
import ph from "node:path";

const BASE = process.env.BASE || "http://127.0.0.1:8080";
const TOKEN = process.env.MERCATOR_API_TOKEN || "dev-token";
const WS = process.env.MERCATOR_WORKSPACE_ID || "ws_1";
const OUT = process.env.OUT || "docs/assets/output";
const W = 1280, H = 800;

const auth = { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json" };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function seedIfEmpty() {
  const res = await fetch(`${BASE}/v1/runs?workspace_id=${WS}`, { headers: auth });
  if (((await res.json()).runs ?? []).length > 0) return;
  execSync("docker pull -q busybox:latest", { stdio: "ignore" });
  const image = execSync("docker inspect --format '{{index .RepoDigests 0}}' busybox:latest").toString().trim();
  const r = await fetch(`${BASE}/v1/runs?workspace_id=${WS}`, {
    method: "POST",
    headers: { ...auth, "Idempotency-Key": "console-demo-seed" },
    body: JSON.stringify({ image, args: ["echo", "hi"] }),
  });
  if (!r.ok) throw new Error(`seed run -> ${r.status}: ${await r.text()}`);
}
await seedIfEmpty();

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  headless: true,
  args: ["--force-color-profile=srgb", "--hide-scrollbars", "--disable-dev-shm-usage"],
});
const context = await browser.newContext({
  viewport: { width: W, height: H },
  deviceScaleFactor: 1,
  colorScheme: "dark",
  recordVideo: { dir: OUT, size: { width: W, height: H } },
});
await context.addInitScript(([token, ws]) => {
  localStorage.setItem("mercator.token", token);
  localStorage.setItem("mercator.workspace", ws);
  localStorage.setItem("mercator.recentWorkspaces", JSON.stringify([ws]));
}, [TOKEN, WS]);

const page = await context.newPage();

// --- injected overlay: pointer sprite, click ripple, and an eased zoom -------
async function installOverlay(atX = 640, atY = 400) {
  await page.evaluate(([ax, ay]) => {
    if (window.__overlay) { window.__cur && window.__cur(ax, ay); return; }
    window.__overlay = true;
    const style = document.createElement("style");
    style.textContent = `
      #root { transition: transform 520ms cubic-bezier(.22,.61,.36,1); will-change: transform; }
      #__cursor { position: fixed; left: 0; top: 0; z-index: 2147483647; pointer-events: none;
        width: 26px; height: 26px; transform: translate(-3px,-2px);
        filter: drop-shadow(0 2px 3px rgba(0,0,0,.45)); }
      #__ripple { position: fixed; z-index: 2147483646; pointer-events: none; left: 0; top: 0;
        width: 12px; height: 12px; margin: -6px 0 0 -6px; border-radius: 50%;
        background: rgba(96,165,250,.35); border: 2px solid rgba(96,165,250,.9); opacity: 0; }
      @keyframes __rip { from { transform: scale(.3); opacity: .9 } to { transform: scale(3.4); opacity: 0 } }`;
    document.head.appendChild(style);
    const c = document.createElement("div");
    c.id = "__cursor";
    c.innerHTML = `<svg width="26" height="26" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
      <path d="M4 2 L4 20 L9 15 L12.4 21.5 L15 20.3 L11.7 14 L18 14 Z"
            fill="#111" stroke="#fff" stroke-width="1.4" stroke-linejoin="round"/></svg>`;
    document.body.appendChild(c);
    const r = document.createElement("div");
    r.id = "__ripple";
    document.body.appendChild(r);
    window.__cur = (x, y) => { c.style.left = x + "px"; c.style.top = y + "px"; };
    window.__glide = (pts, ms) => new Promise((res) => {
      const t0 = performance.now();
      const n = pts.length;
      let done = false;
      const finish = () => { if (done) return; done = true; window.__cur(pts[n - 1].x, pts[n - 1].y); res(); };
      const step = (now) => {
        if (done) return;
        const t = Math.min(1, (now - t0) / ms);
        const f = t * (n - 1), i = Math.min(n - 1, Math.floor(f)), j = Math.min(n - 1, i + 1), k = f - i;
        window.__cur(pts[i].x + (pts[j].x - pts[i].x) * k, pts[i].y + (pts[j].y - pts[i].y) * k);
        if (t < 1) requestAnimationFrame(step); else finish();
      };
      requestAnimationFrame(step);
      setInterval(step, 16);         // keep advancing if rAF throttles
      setTimeout(finish, ms + 400);  // guaranteed resolve
    });
    window.__rip = (x, y) => {
      r.style.left = x + "px"; r.style.top = y + "px";
      r.style.animation = "none"; void r.offsetWidth; r.style.animation = "__rip 520ms ease-out";
    };
    window.__zoom = (s, ox, oy) => {
      const root = document.getElementById("root");
      root.style.transformOrigin = `${ox}px ${oy}px`;
      root.style.transform = `scale(${s})`;
    };
    window.__cur(ax, ay);
  }, [atX, atY]);
}

let cursor = { x: 640, y: 400 };
// Opening a run tears down the injected context mid-animation, so run every
// overlay evaluate through a guard that re-installs the overlay and retries.
async function safeEval(fn, arg) {
  for (let attempt = 0; attempt < 3; attempt++) {
    const present = await page.evaluate(() => !!window.__glide).catch(() => false);
    if (!present) await installOverlay(cursor.x, cursor.y).catch(() => {});
    try {
      return await page.evaluate(fn, arg);
    } catch (e) {
      if (!/context was destroyed|Execution context/.test(String(e))) throw e;
      await page.waitForLoadState("domcontentloaded").catch(() => {});
      await sleep(150);
    }
  }
}
async function glide(x, y, ms = 750) {
  const pts = path(cursor, { x, y }).map((p) => ({ x: p.x, y: p.y }));
  await safeEval(([p, m]) => window.__glide(p, m), [pts, ms]);
  cursor = { x, y };
}
async function ripple(x, y) { await safeEval(([px, py]) => window.__rip(px, py), [x, y]); }
async function zoom(s, ox = 640, oy = 400) { await safeEval(([a, b, c]) => window.__zoom(a, b, c), [s, ox, oy]); }
async function centerOf(locator) {
  const box = await locator.boundingBox();
  return { x: Math.round(box.x + box.width / 2), y: Math.round(box.y + box.height / 2) };
}

// --- choreography -----------------------------------------------------------
await page.goto(`${BASE}/runs?workspace_id=${WS}`, { waitUntil: "domcontentloaded" });
await page.getByText("succeeded", { exact: false }).first().waitFor({ timeout: 15000 });
await installOverlay();
await sleep(1100);

// 1. glide to the first run row, click through to detail.
const row = page.locator("tbody tr").first();
const rc = await centerOf(row);
await glide(rc.x, rc.y, 800);
await ripple(rc.x, rc.y);
await sleep(140);
await row.click();
await page.getByText("Selected offer", { exact: false }).first().waitFor({ timeout: 15000 });
await sleep(500);
await installOverlay(cursor.x, cursor.y);
await sleep(500);

// 2. glide to the selected offer, zoom in to read the decision.
const offer = page.getByText("offer_docker_loopback", { exact: false }).first();
const oc = await centerOf(offer);
await glide(oc.x, oc.y, 750);
await zoom(1.5, oc.x, oc.y);
await sleep(2700);

// 3. zoom out, glide to the Events tab, click it, zoom into the trail.
await zoom(1, oc.x, oc.y);
await sleep(520);
const eventsTab = page.getByRole("tab", { name: "Events" });
const ec = await centerOf(eventsTab);
await glide(ec.x, ec.y, 650);
await ripple(ec.x, ec.y);
await sleep(140);
await eventsTab.click();
await page.getByText("Closed", { exact: false }).first().waitFor({ timeout: 15000 });
const ez = { x: 700, y: 580 }; // upper events-list region
await zoom(1.32, ez.x, ez.y);
await sleep(2600);
await zoom(1, ez.x, ez.y);
await sleep(600);

await context.close();
await browser.close();

const files = fs.readdirSync(OUT).filter((f) => f.endsWith(".webm"));
if (files.length !== 1) { console.error("expected one video, got:", files); process.exit(1); }
fs.renameSync(ph.join(OUT, files[0]), ph.join(OUT, "mercator-demo.webm"));
console.log("wrote", ph.join(OUT, "mercator-demo.webm"));
