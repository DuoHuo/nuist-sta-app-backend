/* 校园地图管理台 SPA —— 原生 JS，无构建步骤。
 * 路由：#/overview #/map #/buildings #/pois #/fingerprints
 * 写接口按后端约定携带 X-Collect-Token（localStorage 持久化）。
 */
"use strict";

/* ---------------- 基础工具 ---------------- */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

/* 全局错误可见化：与其静默白屏，不如弹出提示 */
window.addEventListener("error", e => toast("脚本错误：" + e.message, "err"));
window.addEventListener("unhandledrejection", e =>
  toast("异步错误：" + (e.reason && e.reason.message ? e.reason.message : e.reason), "err"));

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, ch => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
}

function fmtNum(v, digits = 0) {
  if (v === null || v === undefined || v === "") return "—";
  return Number(v).toLocaleString("zh-CN", { maximumFractionDigits: digits });
}

function toast(msg, kind = "ok") {
  const region = $("#toast-region");
  const el = document.createElement("div");
  el.className = `toast ${kind}`;
  el.textContent = msg;
  region.appendChild(el);
  setTimeout(() => el.remove(), 2600);
}

async function api(path, { method = "GET", body } = {}) {
  const headers = { "Accept": "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const token = localStorage.getItem("admin_token");
  if (token) headers["X-Collect-Token"] = token;
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let data = null;
  try { data = await res.json(); } catch { /* GeoJSON 等非包裹响应 */ }
  if (!res.ok) {
    const err = new Error((data && data.message) || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return data && data.code === "ok" ? data.data : data;
}

async function geojson(path) {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

/* ---------------- 弹窗 ---------------- */

const dlg = $("#dlg");
const dlgForm = $("#dlg-form");
const dlgTitle = $("#dlg-title");
const dlgBody = $("#dlg-body");
const dlgError = $("#dlg-error");

function openDialog(title, bodyHTML, onSubmit) {
  dlgTitle.textContent = title;
  dlgBody.innerHTML = bodyHTML;
  dlgError.textContent = "";
  dlgForm.onsubmit = async e => {
    e.preventDefault();
    const btn = $("#dlg-save");
    btn.disabled = true;
    try {
      await onSubmit();
      dlg.close();
    } catch (err) {
      dlgError.textContent = err.status === 401 ? "鉴权失败：请先在左下角设置正确的管理令牌" : (err.message || "保存失败");
    } finally {
      btn.disabled = false;
    }
  };
  dlg.showModal();
  const first = dlgBody.querySelector("input, select, textarea");
  if (first) first.focus();
}
$("#dlg-close").addEventListener("click", () => dlg.close());
dlgForm.addEventListener("click", e => {
  if (e.target.closest("[data-cancel]")) dlg.close();
});

/* 危险操作确认弹窗 */
function confirmDialog(title, message, onConfirm, confirmText = "删除") {
  openDialog(title, `
    <p style="margin:2px 0 4px">${message}</p>
    <p class="cell-sub">此操作立即生效，不可撤销。</p>`, async () => {
    await onConfirm();
  });
  // 把保存按钮替换成危险色
  const btn = $("#dlg-save");
  btn.textContent = confirmText;
  btn.classList.add("btn-danger");
  const restore = () => { btn.textContent = "保存"; btn.classList.remove("btn-danger"); };
  dlg.addEventListener("close", restore, { once: true });
}

/* 地图数据缓存失效：任何要素增删改后调用，下次进入地图页重新拉取 */
function invalidateLayerCache() {
  layerCache = null;
}

/* 令牌设置 */
$("#btn-token").addEventListener("click", () => {
  openDialog("管理令牌", `
    <div class="field">
      <label for="tk">X-Collect-Token</label>
      <input type="text" id="tk" autocomplete="off" placeholder="留空表示服务器未启用令牌"
        value="${esc(localStorage.getItem("admin_token") || "")}">
      <p class="field-error"></p>
      <p class="cell-sub">后端配置 CAMPUS_COLLECT_TOKEN 后写操作需要令牌；令牌仅保存在本浏览器。</p>
    </div>`, async () => {
    const v = $("#tk").value.trim();
    if (v) localStorage.setItem("admin_token", v);
    else localStorage.removeItem("admin_token");
    updateTokenState();
    toast(v ? "令牌已保存" : "令牌已清除");
  });
});
function updateTokenState() {
  $("#token-state").textContent = localStorage.getItem("admin_token") ? "令牌已设置" : "令牌未设置";
}
updateTokenState();

/* ---------------- 路由 ---------------- */

const views = { overview: viewOverview, map: viewMap, buildings: viewBuildings, pois: viewPOIs, fingerprints: viewFingerprints };
let currentView = null;
let mapCleanup = null;

function parseHash() {
  const raw = location.hash.replace(/^#\/?/, "");
  const [viewName, query] = raw.split("?");
  const params = new URLSearchParams(query || "");
  return { name: views[viewName] ? viewName : "overview", params };
}

function route() {
  if (mapCleanup) { mapCleanup(); mapCleanup = null; }
  const { name, params } = parseHash();
  currentView = name;
  $$("#nav a").forEach(a => a.classList.toggle("active", a.dataset.view === name));
  const view = $("#view");
  view.classList.toggle("map-mode", name === "map");
  views[name](view, params);
  view.focus({ preventScroll: true });
}
window.addEventListener("hashchange", route);

/* ---------------- 视图：概览 ---------------- */

async function viewOverview(root) {
  root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
  let s;
  try {
    s = await api("/api/v1/admin/stats");
  } catch (err) {
    root.innerHTML = `<div class="empty"><p>统计加载失败：${esc(err.message)}</p></div>`;
    return;
  }
  const kpi = (v, label, hint = "", accent = false) =>
    `<div class="card kpi${accent ? " accent" : ""}"><div class="kpi-value">${fmtNum(v)}</div><div class="kpi-label">${label}</div>${hint ? `<div class="kpi-hint">${hint}</div>` : ""}</div>`;
  root.innerHTML = `
    <div class="page-head">
      <div><h1>数据概览</h1><div class="sub">地图后端数据资产一览（实时查询）</div></div>
      <div class="head-actions"><a class="btn" href="#/map">打开地图总览</a></div>
    </div>
    <div class="kpi-grid">
      ${kpi(s.buildings, "建筑", `有名称 ${fmtNum(s.buildings_named)} · 带高度 ${fmtNum(s.buildings_with_height)} · 室内图 ${fmtNum(s.buildings_with_indoor_map)}`)}
      ${kpi(s.pois, "可搜索地点")}
      ${kpi(s.nav_nodes, "导航节点")}
      ${kpi(s.nav_edges, "导航边", s.nav_edges_closed ? `封闭 ${fmtNum(s.nav_edges_closed)}` : "全部开放")}
      ${kpi(s.entrances, "建筑出入口")}
      ${kpi(s.floors, "楼层")}
      ${kpi(s.indoor_features, "室内要素")}
      ${kpi(s.fp_sessions, "指纹采集会话", `观测 ${fmtNum(s.fp_observations)} 条`, s.fp_sessions > 0)}
    </div>
    <h2 class="section-title">数据范围</h2>
    <div class="card" style="padding:14px 16px">
      <div style="font-family:var(--mono);font-size:13px">
        ${s.data_extent && s.data_extent.length === 4
          ? `bbox: ${esc(s.data_extent.map(v => Number(v).toFixed(5)).join(", "))}`
          : "（暂无空间数据）"}
      </div>
      <p class="cell-sub" style="margin:6px 0 0">
        建筑 / 路网 / POI 数据来自 OpenStreetMap（© OpenStreetMap contributors），
        高度标注遵循 estimated_floors 约定，室内数据需人工采集后经本管理台或 QGIS 维护。
      </p>
    </div>`;
}

/* ---------------- 视图：地图 ---------------- */

/* 底图：仅自托管 martin（校园瓦片 + configs/style.osm-bright.json，OSM Bright 血统）。
 * 不可达时禁用底图开关，业务图层不受影响。sprite 自托管于 webui/vendor/sprite
 * （MapLibre v5 要求 sprite 用绝对地址）；字形走 openfreemap 公共字体（惰性加载，不阻塞样式就绪）。 */
const PLAIN_STYLE = { version: 8, sources: {}, layers: [
  { id: "bg", type: "background", paint: { "background-color": "#EDF2F9" } },
] };
let brightStyleCache = null;
let labelLang = localStorage.getItem("label_lang") === "en" ? "en" : "zh"; // 地图标签语言

const LAYER_DEFS = [
  { id: "basemap", label: "OSM Bright 底图（自托管）", color: "#7BAFD4", on: true },
  { id: "buildings", label: "建筑轮廓", color: "#1E40AF", on: true },
  { id: "entrances", label: "出入口", color: "#15803D", on: true },
  { id: "edges", label: "路网", color: "#64748B", on: true },
  { id: "pois", label: "地点 POI", color: "#D97706", on: true },
  { id: "nodes", label: "路网节点", color: "#94A3B8", on: false },
];
let layerCache = null;

async function loadLayers() {
  if (layerCache) return layerCache;
  const [buildings, graph, pois] = await Promise.all([
    geojson("/api/v1/admin/buildings/geometry"),
    geojson("/api/v1/admin/graph"),
    geojson("/api/v1/admin/pois/geometry"),
  ]);
  layerCache = {
    buildings: buildings.features.filter(f => f.properties.layer === "building"),
    entrances: buildings.features.filter(f => f.properties.layer === "entrance"),
    edges: graph.features.filter(f => f.properties.layer === "edge"),
    nodes: graph.features.filter(f => f.properties.layer === "node"),
    pois: pois.features,
  };
  return layerCache;
}

/* 轮询等待地图样式就绪；rAF 被浏览器冻结（后台标签页）时样式会长时间不就绪 */
async function waitStyleReady(map, timeoutMs = 25000) {
  const t0 = Date.now();
  while (!map.isStyleLoaded() && Date.now() - t0 < timeoutMs) {
    await new Promise(r => setTimeout(r, 120));
  }
  return map.isStyleLoaded();
}

async function viewMap(root, params) {  root.innerHTML = `
    <div class="map-root">
      <div id="map" role="application" aria-label="校园地图"></div>
      <div class="card map-panel">
        <h3>图层</h3>
        <div class="layer-list" id="layer-list"></div>
        <div class="lang-row" role="group" aria-label="标签语言">
          <span class="lang-title">标签</span>
          <button class="lang-btn" data-lang="zh" type="button" ${labelLang === "zh" ? "active" : ""}>中文</button>
          <button class="lang-btn" data-lang="en" type="button" ${labelLang === "en" ? "active" : ""}>EN</button>
        </div>
        <div class="map-search">
          <div class="search-box">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
            <input type="text" id="map-q" placeholder="搜索建筑并定位…" aria-label="搜索建筑">
          </div>
        </div>
        <div class="map-add-row">
          <button class="btn btn-sm" id="btn-add-poi" type="button" style="width:100%">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>
            添加地点
          </button>
          <div class="map-add-hint" id="add-hint" hidden>在地图上点击要放置的位置，Esc 取消</div>
        </div>
        <p class="legend-note">点击建筑查看详情；高度为估算值的以“估”标注。<br>瓦片与样式自托管 · 数据 © OpenStreetMap contributors</p>
      </div>
    </div>`;

  const center = [118.70695, 32.20275];
  let map;
  try {
    map = new maplibregl.Map({
      container: "map",
      style: PLAIN_STYLE,
      center, zoom: 14.2, minZoom: 3, maxZoom: 19, attributionControl: false,
    });
    // 立即登记兜底清理：若加载中途路由切走/卡住，防止 WebGL 上下文泄漏堆积
    const m0 = map;
    mapCleanup = () => { try { m0.remove(); } catch { /* 已销毁 */ } };
  } catch (err) {
    $("#map").innerHTML = `<div class="empty"><p>地图初始化失败：${esc(err.message)}</p></div>`;
    return;
  }
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");
  map.addControl(new maplibregl.AttributionControl({ compact: true }));
  map.addControl(new maplibregl.ScaleControl({ maxWidth: 90 }), "bottom-left");
  window.__campusMap = map; // 调试用

  let layers;
  try {
    layers = await loadLayers();
  } catch (err) {
    // 失败可重试：不留死页面（半初始化的地图先释放，避免 WebGL 泄漏）
    if (mapCleanup) { mapCleanup(); mapCleanup = null; }
    root.innerHTML = `
      <div class="page-head"><div><h1>地图总览</h1><div class="sub">地图数据加载失败</div></div></div>
      <div class="card" style="padding:36px;text-align:center">
        <p style="margin:0 0 14px">无法连接地图数据服务：${esc(err.message)}</p>
        <button class="btn btn-primary" id="map-retry" type="button">重试</button>
      </div>`;
    $("#map-retry").addEventListener("click", () => viewMap(root, params));
    return;
  }
  // 等待样式就绪：'load' 是一次性事件，若在数据加载期间已发射，
  // 单纯 map.on("load") 会永远等不到；后台标签页 rAF 冻结时样式会迟迟不就绪。
  // 轮询等足够久，仍不就绪给出可重试的失败态，绝不带病往下走。
  if (!(await waitStyleReady(map))) {
    if (mapCleanup) { mapCleanup(); mapCleanup = null; }
    root.innerHTML = `
      <div class="page-head"><div><h1>地图总览</h1><div class="sub">地图样式加载超时</div></div></div>
      <div class="card" style="padding:36px;text-align:center">
        <p style="margin:0 0 14px">地图渲染引擎迟迟未就绪（常见于标签页在后台被浏览器暂停）。切回本页签后点重试即可。</p>
        <button class="btn btn-primary" id="map-retry" type="button">重试</button>
      </div>`;
    $("#map-retry").addEventListener("click", () => viewMap(root, params));
    return;
  }

  const fc = feats => ({ type: "FeatureCollection", features: feats });
  const vis = Object.fromEntries(LAYER_DEFS.map(d => [d.id, d.on])); // 用户图层开关状态

  /* 业务数据图层：setStyle 会清空所有源/图层，切换底图后调用本函数重挂。 */
  function addDataLayers() {
    if (map.getLayer("l-edges")) return;
    map.addSource("s-buildings", { type: "geojson", data: fc(layers.buildings) });
    map.addSource("s-entrances", { type: "geojson", data: fc(layers.entrances) });
    map.addSource("s-edges", { type: "geojson", data: fc(layers.edges) });
    map.addSource("s-nodes", { type: "geojson", data: fc(layers.nodes) });
    map.addSource("s-pois", { type: "geojson", data: fc(layers.pois) });
    map.addSource("s-highlight", { type: "geojson", data: fc([]) });

    map.addLayer({ id: "l-edges", type: "line", source: "s-edges",
      paint: { "line-color": ["case", ["==", ["get", "edge_kind"], "stair"], "#B45309", "#3B6EA5"],
               "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1, 17, 2.4],
               "line-opacity": .75 } });
    map.addLayer({ id: "l-buildings-fill", type: "fill", source: "s-buildings",
      paint: { "fill-color": "#2563EB", "fill-opacity": ["case", ["get", "has_indoor_map"], .42, .22] } });
    map.addLayer({ id: "l-buildings-line", type: "line", source: "s-buildings",
      paint: { "line-color": "#1E40AF", "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1, 17, 2] } });
    map.addLayer({ id: "l-highlight", type: "line", source: "s-highlight",
      paint: { "line-color": "#D97706", "line-width": 3 } });
    map.addLayer({ id: "l-nodes", type: "circle", source: "s-nodes",
      paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 14, 1.5, 18, 4],
               "circle-color": "#94A3B8", "circle-stroke-width": .5, "circle-stroke-color": "#fff" } });
    map.addLayer({ id: "l-entrances", type: "circle", source: "s-entrances",
      paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 2.5, 18, 6],
               "circle-color": "#15803D", "circle-stroke-width": 1.5, "circle-stroke-color": "#fff" } });
    map.addLayer({ id: "l-pois", type: "circle", source: "s-pois",
      paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 3, 18, 7],
               "circle-color": "#D97706", "circle-stroke-width": 1.5, "circle-stroke-color": "#fff" } });

    // 应用用户当前的开关偏好（切换底图后不丢状态）
    const apply = { buildings: ["l-buildings-fill", "l-buildings-line"], entrances: ["l-entrances"],
                    edges: ["l-edges"], pois: ["l-pois"], nodes: ["l-nodes"] };
    for (const [key, ids] of Object.entries(apply)) {
      if (!vis[key]) ids.forEach(id => map.setLayoutProperty(id, "visibility", "none"));
    }
  }
  addDataLayers();

  /* ---- 图层面板 ---- */
  const listEl = $("#layer-list");
  listEl.innerHTML = LAYER_DEFS.map(d => `
    <label class="layer-item"><input type="checkbox" data-layer="${d.id}" ${d.on ? "checked" : ""}>
      <span class="layer-swatch" style="background:${d.color}"></span>${d.label}</label>`).join("");
  const basemapInput = listEl.querySelector('[data-layer=basemap]');

  /* 标签语言切换：改偏好 → 重新适配底图样式（若底图开着） */
  const langRow = root.querySelector(".lang-row");
  langRow.addEventListener("click", e => {
    const btn = e.target.closest("[data-lang]");
    if (!btn || btn.dataset.lang === labelLang) return;
    labelLang = btn.dataset.lang;
    localStorage.setItem("label_lang", labelLang);
    langRow.querySelectorAll(".lang-btn").forEach(b => b.classList.toggle("active", b.dataset.lang === labelLang));
    if (basemapInput.checked && !switching) {
      brightStyleCache = null;
      setBasemap(true);
    }
  });

  const dataLayerMap = { buildings: ["l-buildings-fill", "l-buildings-line"], entrances: ["l-entrances"], edges: ["l-edges"], pois: ["l-pois"], nodes: ["l-nodes"] };
  listEl.addEventListener("change", e => {
    const key = e.target.dataset.layer;
    if (key === "basemap") { setBasemap(e.target.checked); return; }
    vis[key] = e.target.checked;
    (dataLayerMap[key] || []).forEach(id => map.setLayoutProperty(id, "visibility", e.target.checked ? "visible" : "none"));
  });

  /* ---- 底图切换（OpenFreeMap Bright ⇄ 纯色底） ---- */
  let basemapReady = false;   // Bright 样式是否成功加载过
  let switching = false;
  let basemapUnavailable = false;
  window.__basemapDebug = { phase: "init" };
  /* 补操场渲染：Bright 系样式没有 landuse 的 pitch/track/stadium 规则。
   * MapTiler Streets 自带（landuse_stadium），检测到则跳过注入。 */
  function ensureSportLayers(style) {
    if (!style || !Array.isArray(style.layers)) return style;
    const hasPitchRule = style.layers.some(l => l && l.id === "landuse-pitch" ||
      JSON.stringify(l && l.filter || "").includes("\"pitch\""));
    if (hasPitchRule) return style;
    const sport = [
      { id: "landuse-pitch", type: "fill", source: "openmaptiles", "source-layer": "landuse", minzoom: 13,
        filter: ["==", ["get", "class"], "pitch"],
        paint: { "fill-color": "#E8E0B4", "fill-outline-color": "rgba(166,148,88,0.55)" } },
      { id: "landuse-track", type: "fill", source: "openmaptiles", "source-layer": "landuse", minzoom: 13,
        filter: ["==", ["get", "class"], "track"],
        paint: { "fill-color": "#D9A28C", "fill-outline-color": "rgba(163,84,54,0.65)" } },
      { id: "landuse-stadium", type: "fill", source: "openmaptiles", "source-layer": "landuse", minzoom: 12,
        filter: ["match", ["get", "class"], ["stadium", "sports_centre"], true, false],
        paint: { "fill-color": "#E9E3D0", "fill-outline-color": "rgba(150,140,100,0.5)" } },
    ];
    let idx = style.layers.reduce((acc, l, i) => l.id && l.id.startsWith("landcover-") ? i : acc, 0);
    style.layers.splice(idx + 1, 0, ...sport);
    return style;
  }

  /* 底图样式适配：1) 按当前语言改写标签字段；2) 注入建筑名层。
   * MapTiler 系标签是 concat(拉丁转写, 本地文字) 双语写法：
   *   中文模式 → 本地文字（汉字）优先；英文模式 → 拉丁转写优先。
   * 瓦片 building 层无名称字段，楼名以本库命名建筑注入底图样式，
   * 跟随底图开关（关底图即纯业务视图，无任何文字）。 */
  async function adaptBasemapStyle(style) {
    if (!style || !Array.isArray(style.layers)) return style;
    const zhField = ["coalesce", ["get", "name:nonlatin"], ["get", "name"], ["get", "name:latin"]];
    const enField = ["coalesce", ["get", "name:latin"], ["get", "name:nonlatin"], ["get", "name"]];
    for (const l of style.layers) {
      if (l.type !== "symbol" || !l.layout || l.layout["text-field"] == null) continue;
      if (JSON.stringify(l.layout["text-field"]).includes("name:latin")) {
        l.layout["text-field"] = labelLang === "en" ? enField : zhField;
      }
    }
    try {
      const gj = await geojson("/api/v1/admin/buildings/geometry");
      const pts = [];
      const bldNames = [];
      for (const f of gj.features) {
        const p = f.properties || {};
        if (p.layer !== "building" || !p.name || String(p.name).startsWith("未命名建筑")) continue;
        bldNames.push(p.name);
        const ring = f.geometry.coordinates[0];
        let lng = 0, lat = 0;
        for (const c of ring) { lng += c[0]; lat += c[1]; }
        pts.push({
          type: "Feature",
          geometry: { type: "Point", coordinates: [lng / ring.length, lat / ring.length] },
          properties: { name: p.name, name_en: p.name_en || "" },
        });
      }
      // 去重：底图瓦片会给带 amenity/leisure 标签的楼生成 POI 文字标签，
      // 与注入的楼名层重复 —— POI 图层排除与楼名重合的文字（图标保留）
      if (bldNames.length) {
        // 注意：这些图层是旧版过滤器语法，只认 ==/!=/in/!in/all/any/… 等算子，
        // 用 ["!", […]] 取反表达式会导致整个样式校验失败、地图拒载（线上已踩坑）。
        // 瓦片里 POI 的名字可能落在 name / name:latin / name:nonlatin 任一字段
        // （实测 tilemaker 写进 name:latin），三个都要排除。
        const notBuildingName = ["all",
          ["!in", "name", ...bldNames],
          ["!in", "name:latin", ...bldNames],
          ["!in", "name:nonlatin", ...bldNames]];
        for (const l of style.layers) {
          if (l.type !== "symbol" || l["source-layer"] !== "poi") continue;
          l.filter = l.filter ? ["all", l.filter, notBuildingName] : notBuildingName;
        }
      }
      style.sources["campus-building-names"] = { type: "geojson", data: { type: "FeatureCollection", features: pts } };
      style.layers.push({
        id: "basemap-building-name", type: "symbol", source: "campus-building-names", minzoom: 15.5,
        layout: { "text-field": labelLang === "en"
            ? ["coalesce", ["get", "name_en"], ["get", "name"]]
            : ["get", "name"],
          "text-font": ["Noto Sans Regular"],
          "text-size": ["interpolate", ["linear"], ["zoom"], 15.5, 10.5, 19, 13.5],
          "text-letter-spacing": 0.05, "text-padding": 4, "text-allow-overlap": false },
        paint: { "text-color": "#16307A", "text-halo-color": "rgba(255,255,255,0.92)", "text-halo-width": 1.3 },
      });
    } catch { /* 楼名注入失败不影响底图本身 */ }
    return ensureSportLayers(style);
  }

  async function fetchBright() {
    if (brightStyleCache) return brightStyleCache;
    /* 仅自托管：api 同源代理 /martin 拿 OSM Bright（瓦片 = 校园 mbtiles）。
     * 瓦片地址显式指定为同源 /martin/... —— martin 生成的 TileJSON 里
     * tiles 数组不带 /martin 前缀（其内部路径 /campus/...），直接用会 404。
     * sprite 改自托管：外网 sprite（尤其 @2x PNG）拉取极慢会卡死样式加载。 */
    const res = await fetch("/martin/styles/campus", { signal: AbortSignal.timeout(4000) });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const style = await res.json();
    const tj = await fetch("/martin/campus", { signal: AbortSignal.timeout(4000) })
      .then(r => { if (!r.ok) throw new Error(`HTTP ${r.status}`); return r.json(); });
    style.sources.openmaptiles = {
      type: "vector",
      tiles: [`${location.origin}/martin/campus/{z}/{x}/{y}`],
      bounds: tj.bounds,
      minzoom: tj.minzoom ?? 0,
      maxzoom: tj.maxzoom ?? 14,
      attribution: "© OpenStreetMap contributors",
    };
    style.sprite = `${location.origin}/admin/vendor/sprite`;
    brightStyleCache = await adaptBasemapStyle(style);
    return brightStyleCache;
  }
  function waitForStyleLoad() {
    return new Promise(resolve => {
      const done = () => { map.off("styledata", check); clearInterval(poll); clearTimeout(timer); resolve(); };
      const check = () => { if (map.isStyleLoaded()) done(); };
      const poll = setInterval(check, 150);
      // 外网瓦片源的 tilejson 拉取可能超过 10s，兜底放宽
      const timer = setTimeout(done, 20000);
      map.on("styledata", check);
      check();
    });
  }
  async function setBasemap(on) {
    if (switching) return;
    switching = true;
    basemapInput.disabled = true;
    try {
      let style = PLAIN_STYLE;
      window.__basemapDebug = { phase: "fetching" };
      if (on) style = await fetchBright();
      if (basemapInput.checked !== on) basemapInput.checked = on; // 与请求保持一致
      window.__basemapDebug = { phase: "setStyle" };
      map.setStyle(structuredClone(style), { diff: false });
      await waitForStyleLoad();
      if (!(await waitStyleReady(map, 15000))) throw new Error("底图样式加载超时");
      window.__basemapDebug = { phase: "addDataLayers" };
      addDataLayers();
      basemapReady = on;
      window.__basemapDebug = { phase: "done", on };
    } catch (err) {
      basemapUnavailable = true;
      basemapInput.checked = false;
      basemapInput.closest(".layer-item").title = "自托管瓦片服务不可达";
      toast("底图样式加载失败：" + err.message + "，已回退纯色底", "err");
      window.__basemapDebug = { phase: "error", msg: String(err && err.message || err) };
    } finally {
      switching = false;
      basemapInput.disabled = basemapUnavailable;
    }
  }
  // 默认开启 Bright：后台拉样式，成功即切换（首屏先用纯色底渲染业务图层）
  setBasemap(true);

  /* 高缩放的 POI 名称用 DOM 标注（避免依赖字形服务） */
  const poiMarkers = [];
  function refreshPoiLabels() {
    const poisBox = $("#layer-list [data-layer=pois]");
    const show = map.getZoom() >= 15.2 && poisBox && poisBox.checked;
    if (!show) { poiMarkers.forEach(m => m.remove()); poiMarkers.length = 0; return; }
    const bounds = map.getBounds();
    const inView = layers.pois.filter(f => {
      const [lng, lat] = f.geometry.coordinates;
      return bounds.contains([lng, lat]);
    }).slice(0, 120);
    poiMarkers.forEach(m => m.remove()); poiMarkers.length = 0;
    for (const f of inView) {
      const div = document.createElement("div");
      div.className = "poi-label";
      div.textContent = f.properties.name;
      poiMarkers.push(new maplibregl.Marker({ element: div, anchor: "left", offset: [8, 0] })
        .setLngLat(f.geometry.coordinates).addTo(map));
    }
  }
  map.on("zoomend", refreshPoiLabels);
  listEl.addEventListener("change", () => setTimeout(refreshPoiLabels, 0));

  const popup = new maplibregl.Popup({ maxWidth: "280px", closeButton: false });
  let addMode = false;

  /* 增删改后刷新地图上的 POI / 建筑数据源（含模块级缓存） */
  async function refreshPoisOnMap() {
    poisCache = null;
    const gj = await geojson("/api/v1/admin/pois/geometry");
    layers.pois = gj.features;
    layerCache = layers;
    const src = map.getSource("s-pois");
    if (src) src.setData({ type: "FeatureCollection", features: layers.pois });
    refreshPoiLabels();
  }
  async function refreshBuildingsOnMap() {
    const gj = await geojson("/api/v1/admin/buildings/geometry");
    layers.buildings = gj.features.filter(f => f.properties.layer === "building");
    layers.entrances = gj.features.filter(f => f.properties.layer === "entrance");
    layerCache = layers;
    const sb = map.getSource("s-buildings"), se = map.getSource("s-entrances");
    if (sb) sb.setData({ type: "FeatureCollection", features: layers.buildings });
    if (se) se.setData({ type: "FeatureCollection", features: layers.entrances });
    buildingsCache = null;
    brightStyleCache = null; // 底图里的楼名层数据已变，下次切换底图时重新生成
  }

  function wirePopupActions() {
    const el = popup.getElement();
    if (!el) return;
    el.querySelector("[data-act=del-poi]")?.addEventListener("click", () => {
      const id = Number(el.querySelector("[data-act=del-poi]").dataset.id);
      popup.remove();
      confirmDialog("删除地点", "确定删除这个地点吗？", async () => {
        await api(`/api/v1/admin/pois/${id}`, { method: "DELETE" });
        toast("地点已删除");
        await refreshPoisOnMap();
      });
    });
    el.querySelector("[data-act=del-building]")?.addEventListener("click", () => {
      const id = el.querySelector("[data-act=del-building]").dataset.id;
      popup.remove();
      confirmDialog("删除建筑", `确定删除建筑 <b>${esc(id)}</b> 吗？其楼层、室内要素、出入口与内部路网节点将一并删除。`, async () => {
        await api(`/api/v1/admin/buildings/${encodeURIComponent(id)}`, { method: "DELETE" });
        toast("建筑已删除");
        await refreshBuildingsOnMap();
      });
    });
  }

  map.on("click", "l-buildings-fill", e => {
    if (addMode) return;
    const p = e.features[0].properties;
    const height = p.height_m ? `${Number(p.height_m).toFixed(1)} m${p.height_source === "estimated_floors" ? "（估）" : ""}` : "—";
    popup.setLngLat(e.lngLat).setHTML(`
      <h4>${esc(p.name)}</h4>
      <div class="kv">ID ${esc(p.building_id)}</div>
      <div class="kv">高度 ${esc(height)} · ${p.has_indoor_map ? "有室内图" : "无室内图"}</div>
      <div class="popup-actions">
        <a class="btn btn-sm" href="#/buildings?edit=${encodeURIComponent(p.building_id)}">编辑档案</a>
        <button class="btn btn-sm btn-danger" data-act="del-building" data-id="${esc(p.building_id)}" type="button">删除</button>
      </div>`).addTo(map);
    wirePopupActions();
  });
  map.on("click", "l-pois", e => {
    if (addMode) return;
    const p = e.features[0].properties;
    popup.setLngLat(e.lngLat).setHTML(`
      <h4>${esc(p.name)}</h4>
      <div class="kv">${esc(p.category || "未分类")}${p.building_id ? " · " + esc(p.building_id) : ""}</div>
      <div class="popup-actions">
        <a class="btn btn-sm" href="#/pois?edit=${p.poi_id}">编辑地点</a>
        <button class="btn btn-sm btn-danger" data-act="del-poi" data-id="${p.poi_id}" type="button">删除</button>
      </div>`).addTo(map);
    wirePopupActions();
  });
  for (const id of ["l-buildings-fill", "l-pois", "l-entrances"]) {
    map.on("mouseenter", id, () => { if (!addMode) map.getCanvas().style.cursor = "pointer"; });
    map.on("mouseleave", id, () => (map.getCanvas().style.cursor = ""));
  }

  /* 添加地点模式：点选坐标 → 填表 → POST */
  const addBtn = $("#btn-add-poi");
  const addHint = $("#add-hint");
  function setAddMode(on) {
    addMode = on;
    addBtn.classList.toggle("active", on);
    addHint.hidden = !on;
    map.getCanvas().style.cursor = on ? "crosshair" : "";
  }
  addBtn.addEventListener("click", () => setAddMode(!addMode));
  document.addEventListener("keydown", function escQuit(e) {
    if (e.key === "Escape" && addMode && location.hash.includes("#/map")) setAddMode(false);
    if (mapCleanup === null) document.removeEventListener("keydown", escQuit);
  });
  map.on("click", e => {
    if (!addMode) return;
    const { lng, lat } = e.lngLat;
    setAddMode(false);
    openDialog("添加地点", `
      <div class="field"><label for="np-name">名称 <span class="hint">（必填）</span></label>
        <input type="text" id="np-name" placeholder="如: 东苑食堂 / 北门" required><p class="field-error"></p></div>
      <div class="field"><label for="np-cat">分类</label>
        <input type="text" id="np-cat" placeholder="如: 食堂 / 出入口 / 地标" list="np-cats">
        <datalist id="np-cats"><option value="出入口"></option><option value="地标"></option><option value="食堂"></option><option value="教学楼"></option><option value="宿舍"></option><option value="运动场"></option><option value="服务"></option></datalist>
        <p class="field-error"></p></div>
      <div class="field"><label for="np-kw">搜索关键词 <span class="hint">（逗号分隔，可留空）</span></label>
        <input type="text" id="np-kw"><p class="field-error"></p></div>
      <div class="field"><label>坐标</label><input type="text" value="${lng.toFixed(6)}, ${lat.toFixed(6)}" readonly style="background:var(--muted);font-family:var(--mono)"></div>`, async () => {
      const name = $("#np-name").value.trim();
      if (!name) throw new Error("名称不能为空");
      const cat = $("#np-cat").value.trim();
      const kwRaw = $("#np-kw").value.trim();
      const keywords = kwRaw ? kwRaw.split(/[,，、\s]+/).filter(Boolean) : [];
      const d = await api("/api/v1/admin/pois", {
        method: "POST",
        body: { name, category: cat, keywords, lng, lat },
      });
      toast(`地点已添加（#${d.poi_id}）`);
      await refreshPoisOnMap();
    });
  });

  function focusBuilding(buildingId) {
    const feats = layers.buildings.filter(f => f.properties.building_id === buildingId ||
                                               f.properties.name === buildingId);
    if (!feats.length) { toast("未找到该建筑", "err"); return; }
    const coords = feats[0].geometry.coordinates.flat(2);
    const lngs = coords.map(c => c[0]), lats = coords.map(c => c[1]);
    map.fitBounds([[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]],
      { padding: 90, maxZoom: 17.5, duration: 700 });
    const src = map.getSource("s-highlight");
    if (src) src.setData(fc(feats));
  }
  $("#map-q").addEventListener("keydown", e => {
    if (e.key === "Enter") { e.preventDefault(); const v = e.target.value.trim(); if (v) focusBuilding(v); }
  });

  if (params.get("focus")) focusBuilding(params.get("focus"));
  refreshPoiLabels();

  mapCleanup = () => { popup.remove(); poiMarkers.forEach(m => m.remove()); map.remove(); };
}

/* ---------------- 视图：建筑 ---------------- */
/* ---------------- 视图：建筑 ---------------- */

let buildingsCache = null;

async function loadBuildings() {
  if (buildingsCache) return buildingsCache;
  const d = await api("/api/v1/buildings");
  buildingsCache = d.buildings || [];
  return buildingsCache;
}

const PAGE_SIZE = 15;

async function viewBuildings(root, params) {
  root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
  let buildings;
  try {
    buildings = await loadBuildings();
  } catch (err) {
    root.innerHTML = `<div class="empty"><p>建筑列表加载失败：${esc(err.message)}</p></div>`;
    return;
  }

  let keyword = "", page = 1;
  root.innerHTML = `
    <div class="page-head">
      <div><h1>建筑管理</h1><div class="sub">${buildings.length} 栋建筑 · 编辑名称 / 别名 / 高度来源等档案字段</div></div>
    </div>
    <div class="toolbar">
      <div class="search-box">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input type="text" id="b-q" placeholder="按名称 / 编号 / ID 过滤…" aria-label="过滤建筑">
      </div>
      <span class="result-count" id="b-count"></span>
    </div>
    <div class="card table-wrap" style="max-height:calc(100vh - 210px)">
      <table aria-label="建筑列表">
        <thead><tr>
          <th>名称</th><th>建筑 ID</th><th>别名</th><th class="num">高度</th><th>高度来源</th><th>室内图</th><th></th>
        </tr></thead>
        <tbody id="b-body"></tbody>
      </table>
      <div class="empty" id="b-empty" hidden>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <p>没有匹配的建筑</p>
      </div>
    </div>
    <div class="pager" id="b-pager"></div>`;

  const sourceBadge = { measured: '<span class="badge ok">实测</span>', estimated_floors: '<span class="badge warn">按层估算</span>', display_default: '<span class="badge">展示默认</span>' };

  function render() {
    const kw = keyword.trim().toLowerCase();
    const filtered = kw
      ? buildings.filter(b => [b.name, b.building_id, ...(b.aliases || [])].join(" ").toLowerCase().includes(kw))
      : buildings;
    const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
    page = Math.min(page, pages);
    const rows = filtered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);
    $("#b-body").innerHTML = rows.map(b => `
      <tr data-id="${esc(b.building_id)}">
        <td><strong>${esc(b.name)}</strong></td>
        <td class="cell-id">${esc(b.building_id)}</td>
        <td class="cell-sub">${esc((b.aliases || []).join("、") || "—")}</td>
        <td class="num">${b.height_m != null ? Number(b.height_m).toFixed(1) + " m" : "—"}</td>
        <td>${sourceBadge[b.height_source] || "—"}</td>
        <td>${b.has_indoor_map ? '<span class="badge blue">有</span>' : '<span class="badge">无</span>'}</td>
        <td><div class="row-actions">
          <button class="icon-btn" data-act="locate" title="在地图中定位" aria-label="在地图中定位 ${esc(b.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></svg>
          </button>
          <button class="icon-btn" data-act="edit" title="编辑档案" aria-label="编辑 ${esc(b.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
          </button>
          <button class="icon-btn" data-act="del" title="删除建筑" aria-label="删除 ${esc(b.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
          </button>
        </div></td>
      </tr>`).join("");
    $("#b-empty").hidden = filtered.length > 0;
    $("#b-count").textContent = `共 ${filtered.length} 条`;
    $("#b-pager").innerHTML = `
      <button class="btn btn-sm" data-p="prev" ${page <= 1 ? "disabled" : ""}>上一页</button>
      <span>第 ${page} / ${pages} 页</span>
      <button class="btn btn-sm" data-p="next" ${page >= pages ? "disabled" : ""}>下一页</button>`;
  }

  $("#b-q").addEventListener("input", e => { keyword = e.target.value; page = 1; render(); });
  $("#b-pager").addEventListener("click", e => {
    const p = e.target.closest("button")?.dataset.p;
    if (p === "prev") page--;
    if (p === "next") page++;
    render();
  });
  $("#b-body").addEventListener("click", async e => {
    const btn = e.target.closest("button[data-act]");
    if (!btn) return;
    const id = btn.closest("tr").dataset.id;
    if (btn.dataset.act === "locate") {
      location.hash = `#/map?focus=${encodeURIComponent(id)}`;
    } else if (btn.dataset.act === "del") {
      confirmDialog("删除建筑", `确定删除建筑 <b>${esc(id)}</b> 吗？其楼层、室内要素、出入口与内部路网节点将一并删除。`, async () => {
        await api(`/api/v1/admin/buildings/${encodeURIComponent(id)}`, { method: "DELETE" });
        toast("建筑已删除");
        buildingsCache = null;
        invalidateLayerCache();
        buildings = await loadBuildings();
        render();
      });
    } else {
      const b = buildings.find(x => x.building_id === id);
      if (b) editBuildingDialog(b, async () => {
        buildingsCache = null;
        buildings = await loadBuildings();
        invalidateLayerCache();
        render();
      });
    }
  });

  render();
  const editId = params.get("edit");
  if (editId) {
    const b = buildings.find(x => x.building_id === editId);
    if (b) editBuildingDialog(b, async () => {
      buildingsCache = null;
      buildings = await loadBuildings();
      render();
    });
  }
}

function editBuildingDialog(b, onSaved) {
  openDialog(`编辑建筑 · ${b.name}`, `
    <div class="field"><label for="f-name">名称 <span class="hint">（必填）</span></label>
      <input type="text" id="f-name" value="${esc(b.name)}" required><p class="field-error"></p></div>
    <div class="field"><label for="f-alias">别名 <span class="hint">（逗号分隔，可留空）</span></label>
      <input type="text" id="f-alias" value="${esc((b.aliases || []).join(","))}" placeholder="如: 文园10, W10"><p class="field-error"></p></div>
    <div class="field"><label for="f-height">高度（米） <span class="hint">（留空表示清除）</span></label>
      <input type="number" id="f-height" step="0.1" min="0" value="${b.height_m != null ? Number(b.height_m).toFixed(1) : ""}" placeholder="如 19.2"><p class="field-error"></p></div>
    <div class="field"><label for="f-source">高度来源 <span class="hint">（填写高度时必选）</span></label>
      <select id="f-source">
        <option value="">— 清除 —</option>
        <option value="measured">measured（实测）</option>
        <option value="estimated_floors">estimated_floors（按楼层估算）</option>
        <option value="display_default">display_default（展示默认）</option>
      </select><p class="field-error"></p></div>
    <div class="field"><label class="check-row"><input type="checkbox" id="f-indoor" ${b.has_indoor_map ? "checked" : ""}> 有室内地图数据</label></div>
    <section class="building-model" id="building-model" aria-label="建筑三维模型"></section>
  `, async () => {
    const name = $("#f-name").value.trim();
    if (!name) throw new Error("名称不能为空");
    const aliasRaw = $("#f-alias").value.trim();
    const aliases = aliasRaw ? aliasRaw.split(/[,，、\s]+/).filter(Boolean) : [];
    const heightRaw = $("#f-height").value.trim();
    const source = $("#f-source").value;
    const body = { name, aliases, has_indoor_map: $("#f-indoor").checked };
    if (heightRaw === "") {
      if (b.height_m != null) { body.clear_height = true; body.height_source = ""; }
    } else {
      const h = Number(heightRaw);
      if (!(h > 0)) throw new Error("高度必须为正数");
      if (!source) throw new Error("填写高度时必须选择高度来源");
      body.height_m = h;
      body.height_source = source;
    }
    if (b.height_m != null && heightRaw !== "" && !source) body.height_source = "";
    await api(`/api/v1/admin/buildings/${encodeURIComponent(b.building_id)}`, { method: "PATCH", body });
    toast("建筑档案已保存");
    await onSaved?.();
  });
  const sel = $("#f-source");
  sel.value = b.height_source || "";
  window.CampusModels.attach($("#building-model"), b);
}

/* ---------------- 视图：地点 ---------------- */

let poisCache = null;
async function loadPOIs() {
  if (poisCache) return poisCache;
  const d = await api("/api/v1/pois?limit=100");
  poisCache = d.pois || [];
  if (poisCache.length < d.count) { // 超过 100 条时走管理端全量接口
    poisCache = (await geojson("/api/v1/admin/pois/geometry")).features
      .map(f => ({ poi_id: f.properties.poi_id, name: f.properties.name,
                   category: f.properties.category, building_id: f.properties.building_id,
                   keywords: [], lng: f.geometry.coordinates[0], lat: f.geometry.coordinates[1] }));
  }
  return poisCache;
}

async function viewPOIs(root, params) {
  root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
  let pois;
  try {
    pois = await loadPOIs();
  } catch (err) {
    root.innerHTML = `<div class="empty"><p>地点加载失败：${esc(err.message)}</p></div>`;
    return;
  }
  const categories = [...new Set(pois.map(p => p.category).filter(Boolean))].sort();

  let keyword = "", category = "";
  root.innerHTML = `
    <div class="page-head"><div><h1>地点管理</h1><div class="sub">${pois.length} 个可搜索地点（POI）</div></div></div>
    <div class="toolbar">
      <div class="search-box">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input type="text" id="p-q" placeholder="按名称过滤…" aria-label="过滤地点">
      </div>
      <select id="p-cat" aria-label="按分类过滤"><option value="">全部分类</option>
        ${categories.map(c => `<option value="${esc(c)}">${esc(c)}</option>`).join("")}</select>
      <span class="result-count" id="p-count"></span>
    </div>
    <div class="card table-wrap" style="max-height:calc(100vh - 210px)">
      <table aria-label="地点列表">
        <thead><tr><th>ID</th><th>名称</th><th>分类</th><th>所属建筑</th><th>坐标</th><th></th></tr></thead>
        <tbody id="p-body"></tbody>
      </table>
      <div class="empty" id="p-empty" hidden><p>没有匹配的地点</p></div>
    </div>
    <div class="empty" id="p-none" hidden>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></svg>
      <p>还没有任何地点 —— 去 <a href="#/map">地图总览</a> 点选位置添加，或等 App 端数据接入</p>
    </div>`;

  function render() {
    const kw = keyword.trim().toLowerCase();
    const filtered = pois.filter(p =>
      (!kw || p.name.toLowerCase().includes(kw)) && (!category || p.category === category));
    $("#p-none").hidden = pois.length > 0;
    $("#p-body").innerHTML = filtered.map(p => `
      <tr data-id="${p.poi_id}">
        <td class="cell-id">${p.poi_id}</td>
        <td><strong>${esc(p.name)}</strong>${p.keywords && p.keywords.length ? `<div class="cell-sub">${esc(p.keywords.filter(k => k !== "osm-import").join("、"))}</div>` : ""}</td>
        <td>${p.category ? `<span class="badge blue">${esc(p.category)}</span>` : "—"}</td>
        <td class="cell-id">${esc(p.building_id || "—")}</td>
        <td class="cell-id">${p.lng != null ? p.lng.toFixed(5) + ", " + p.lat.toFixed(5) : "—"}</td>
        <td><div class="row-actions">
          <button class="icon-btn" data-act="edit" title="编辑地点" aria-label="编辑 ${esc(p.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
          </button>
          <button class="icon-btn" data-act="del" title="删除地点" aria-label="删除 ${esc(p.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
          </button>
        </div></td>
      </tr>`).join("");
    $("#p-empty").hidden = filtered.length > 0;
    $("#p-count").textContent = `共 ${filtered.length} 条`;
  }

  $("#p-q").addEventListener("input", e => { keyword = e.target.value; render(); });
  $("#p-cat").addEventListener("change", e => { category = e.target.value; render(); });
  $("#p-body").addEventListener("click", e => {
    const btn = e.target.closest("button[data-act]");
    if (!btn) return;
    const id = Number(btn.closest("tr").dataset.id);
    const p = pois.find(x => x.poi_id === id);
    if (!p) return;
    if (btn.dataset.act === "del") {
      confirmDialog("删除地点", `确定删除地点 <b>${esc(p.name)}</b> 吗？`, async () => {
        await api(`/api/v1/admin/pois/${p.poi_id}`, { method: "DELETE" });
        toast("地点已删除");
        poisCache = null;
        invalidateLayerCache();
        pois = await loadPOIs();
        render();
      });
    } else {
      editPOIDialog(p, async () => {
        poisCache = null;
        pois = await loadPOIs();
        invalidateLayerCache();
        render();
      });
    }
  });

  render();
  const editId = params.get("edit");
  if (editId) {
    const p = pois.find(x => String(x.poi_id) === editId);
    if (p) editPOIDialog(p, async () => {
      poisCache = null;
      pois = await loadPOIs();
      invalidateLayerCache();
      render();
    });
  }
}

function editPOIDialog(p, onSaved) {
  openDialog(`编辑地点 · ${p.name}`, `
    <div class="field"><label for="pf-name">名称 <span class="hint">（必填）</span></label>
      <input type="text" id="pf-name" value="${esc(p.name)}" required><p class="field-error"></p></div>
    <div class="field"><label for="pf-cat">分类</label>
      <input type="text" id="pf-cat" value="${esc(p.category || "")}" placeholder="如: 出入口 / 食堂 / 公交站" list="pf-cats">
      <datalist id="pf-cats"><option value="出入口"></option><option value="地标"></option><option value="公交站"></option><option value="食堂"></option><option value="教室"></option></datalist>
      <p class="field-error"></p></div>
    <div class="field"><label for="pf-kw">搜索关键词 <span class="hint">（逗号分隔，可留空）</span></label>
      <input type="text" id="pf-kw" value="${esc((p.keywords || []).join(","))}"><p class="field-error"></p></div>
  `, async () => {
    const name = $("#pf-name").value.trim();
    if (!name) throw new Error("名称不能为空");
    const cat = $("#pf-cat").value.trim();
    const kwRaw = $("#pf-kw").value.trim();
    const keywords = kwRaw ? kwRaw.split(/[,，、\s]+/).filter(Boolean) : [];
    await api(`/api/v1/admin/pois/${p.poi_id}`, {
      method: "PATCH",
      body: { name, category: cat, clear_category: !cat, keywords },
    });
    toast("地点已保存");
    await onSaved?.();
  });
}

/* ---------------- 视图：指纹 ---------------- */

async function viewFingerprints(root) {
  root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
  let buildings, sessions;
  try {
    [buildings, sessions] = await Promise.all([
      loadBuildings(),
      api("/api/v1/fingerprints"),
    ]);
  } catch (err) {
    root.innerHTML = `<div class="empty"><p>指纹数据加载失败：${esc(err.message)}</p></div>`;
    return;
  }
  const list = sessions.fingerprints || [];
  const withFP = buildings.filter(b => list.some(s => s.building_id === b.building_id));

  root.innerHTML = `
    <div class="page-head"><div><h1>指纹采集记录</h1><div class="sub">${list.length} 个会话（只读；采集通过 App 端 POST /fingerprints 上报）</div></div></div>
    <div class="toolbar">
      <select id="fp-b" aria-label="按建筑过滤"><option value="">全部建筑</option>
        ${withFP.map(b => `<option value="${esc(b.building_id)}">${esc(b.name)}</option>`).join("")}</select>
      <span class="result-count" id="fp-count"></span>
    </div>
    <div class="card table-wrap" style="max-height:calc(100vh - 210px)">
      <table aria-label="指纹会话列表">
        <thead><tr><th class="num">会话</th><th>建筑</th><th>楼层</th><th class="num">位置 (x,y m)</th><th>设备</th><th class="num">AP 数</th><th>采集时间</th></tr></thead>
        <tbody id="fp-body"></tbody>
      </table>
      <div class="empty" id="fp-empty" ${list.length ? "hidden" : ""}>
        <p>暂无指纹数据 —— 在 App 端采集并上报后，这里会显示会话记录</p>
      </div>
    </div>`;

  let buildingId = "";
  function render() {
    const rows = list.filter(s => !buildingId || s.building_id === buildingId);
    $("#fp-body").innerHTML = rows.map(s => `
      <tr>
        <td class="cell-id">#${s.session_id}</td>
        <td>${esc(s.building_name || s.building_id || "—")}</td>
        <td>${esc(s.floor_name || "—")}</td>
        <td class="num">${s.x_m != null ? `${Number(s.x_m).toFixed(1)}, ${Number(s.y_m).toFixed(1)}` : "—"}</td>
        <td class="cell-sub">${esc(s.device_model || "—")}</td>
        <td class="num">${s.obs_count}</td>
        <td class="cell-id">${esc(String(s.captured_at || "").replace("T", " ").slice(0, 19))}</td>
      </tr>`).join("");
    $("#fp-empty").hidden = rows.length > 0;
    $("#fp-count").textContent = `共 ${rows.length} 条`;
  }
  $("#fp-b").addEventListener("change", e => { buildingId = e.target.value; render(); });
  render();
}

/* ---------------- 启动 ---------------- */

route();
