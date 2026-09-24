/* 校园地图管理台 SPA —— 原生 JS，无构建步骤。
 *
 * 路由（hash）：#/overview #/map #/buildings #/pois #/features #/fingerprints #/bus
 * 写接口按后端约定携带 X-Collect-Token（localStorage 持久化）。
 *
 * 结构：
 *   1. 框架层：esc / toast / api / openDialog / confirmDialog / hash 路由；
 *   2. 缓存与失效：listCaches（列表页）+ geoMemos（几何取数去重）+ dataChanged()，
 *      写操作成功后一律调 dataChanged(...)，由它统一清缓存并通知地图重取；
 *   3. 视图：一个视图 = 一个 async function(root, params)，登记在 views 表；
 *   4. 地图插件：地图页不再硬编码业务图层，图层由 registerMapPlugin 注册
 *      （一个插件 = 数据源 + 图层定义 + 点击弹窗 + 可选轮询），见下方「地图插件」段；
 *   5. 扩展接口：AdminKit 暴露给独立模块文件（admin-bus.js / admin-models.js 是范例）。
 *
 * 两条跨模块约定：
 *   - 只通过 dataChanged(...) 表达"数据变了"，不要各自去 null 缓存；
 *   - 新页面走 registerView，新图层走 registerMapPlugin，新绘制任务走 registerDrawTask。
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

/* ---------------- 缓存与数据失效 ---------------- */

/* 列表页缓存：key -> 数据（null = 下次进页面重新拉）。
 * 键名与 map plugin 的 dataKey 对齐，dataChanged("buildings") 一次清两处。 */
const listCaches = { buildings: null, pois: null, features: null };

/* 几何取数去重：多个图层常读同一个端点（建筑+出入口同源、路网边+节点同源），
 * 同一轮里只发一次请求。key 与 dataKey 无关，纯粹按端点去重。 */
const geoMemos = {};
function geoOnce(key, path) {
  if (!geoMemos[key]) {
    geoMemos[key] = geojson(path).catch(err => { delete geoMemos[key]; throw err; });
  }
  return geoMemos[key];
}

/* 地图挂载期间登记的就地刷新钩子：pluginId -> async fn()。
 * 未挂载时表是空的，dataChanged 自然变成"只清缓存"——下次进地图页重新拉取。 */
let mapPluginsLive = {};

/* 模块注册的额外清理器（如 admin-bus.js 自己的列表缓存） */
const dataDroppers = [];
function onDataChanged(fn) { dataDroppers.push(fn); }

/* 写操作成功后的唯一入口。kind 用 dataKey：buildings / pois / features / bus / all。 */
function dataChanged(...kinds) {
  const all = kinds.length === 0 || kinds.includes("all");
  const hit = k => all || kinds.includes(k);
  for (const key of Object.keys(listCaches)) if (hit(key)) listCaches[key] = null;
  for (const key of Object.keys(geoMemos)) delete geoMemos[key];
  layerCache = null;
  for (const p of mapPlugins) {
    const key = p.dataKey || p.id;
    if (hit(key)) mapPluginsLive[p.id]?.();
  }
  dataDroppers.forEach(fn => fn(hit));
}

/* 令牌设置 */
$("#btn-token").addEventListener("click", () => {
  openDialog("管理令牌", `
    <div class="field">
      <label for="tk">X-Collect-Token</label>
      <input type="text" id="tk" autocomplete="off" placeholder="粘贴服务端 CAMPUS_COLLECT_TOKEN 里的令牌部分"
        value="${esc(localStorage.getItem("admin_token") || "")}">
      <p class="field-error"></p>
      <p class="cell-sub">服务端未配置令牌时写接口一律拒绝（fail-closed）。配置成「名字:令牌」时，
        这里只填令牌部分，名字由服务端记入 created_by。令牌仅保存在本浏览器。</p>
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

const views = { overview: viewOverview, map: viewMap, buildings: viewBuildings, pois: viewPOIs, features: viewFeatures, fingerprints: viewFingerprints };
let currentView = null;
let mapCleanup = null;

/* 视图表：内置视图写在上面；独立模块（admin-bus.js 等）用 registerView 追加 */
function registerView(name, fn) { views[name] = fn; }

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
      ${kpi(s.map_features, "通用地物", "道路 / 绿地 / 广场等，管理台提交")}
      ${kpi(s.nav_nodes, "导航节点")}
      ${kpi(s.nav_edges, "导航边", s.nav_edges_closed ? `封闭 ${fmtNum(s.nav_edges_closed)}` : "全部开放")}
      ${kpi(s.entrances, "建筑出入口")}
      ${kpi(s.floors, "楼层")}
      ${kpi(s.indoor_features, "室内要素")}
      ${kpi(s.fp_sessions, "指纹采集会话", `观测 ${fmtNum(s.fp_observations)} 条`, s.fp_sessions > 0)}
      ${kpi(s.bus_routes, "公交线路", `${fmtNum(s.bus_stops)} 个站点 · ${fmtNum(s.bus_vehicles)} 辆车`)}
      ${kpi(s.bus_positions_today, "今日车辆位置", "实时位置为时序数据，7 天前的建议清理", s.bus_positions_today > 0)}
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

/* ---------------- 地图插件（图层注册表） ----------------
 * 一个插件 = 一份数据 + 若干图层 + 点击弹窗 + 可选轮询。声明式：地图页按这张表
 * 挂源、挂层、绑事件，加一类图层不需要再动 loadLayers / addDataLayers / 面板 / 刷新四处。
 *   id          图层面板里的开关 key，也是 dataChanged 的默认匹配键
 *   dataKey     失效分组：公交三个图层共用 "bus"，缺省等于 id
 *   load()      取数，返回要素数组；失败向上抛，由地图页统一显示失败态
 *   layers[]    MapLibre 图层定义；source 由地图页按插件自动绑定为 s-<id>
 *   popup(f)    点击弹窗 HTML（f 是要素）；不写就没有点击与手型光标
 *   remove(f)   弹窗里 data-act="remove" 按钮的行为（自己负责确认与刷新）
 *   focusId(f)  支持 #/map?focus=<id> 定位到该要素
 *   refreshMs   轮询间隔（实时车辆用）
 * 注册顺序 = 叠放顺序（先注册的在下面）；面板显示顺序见 LAYER_ORDER。 */
const mapPlugins = [];
function registerMapPlugin(plugin) { mapPlugins.push(plugin); }

/* 面板顺序：底图永远第一，其余按此表；表外的插件（如公交）按注册顺序追加 */
const LAYER_ORDER = ["basemap", "buildings", "entrances", "edges", "pois", "features", "nodes"];
const BASEMAP_DEF = { id: "basemap", label: "OSM Bright 底图（自托管）", color: "#7BAFD4", on: true };

let layerCache = null; // { pluginId: 要素数组 }

/* ---------------- 绘制任务（URL 驱动） ----------------
 * 内置任务：poi / point / polygon / building / redraw / redraw-feature（面板按钮与列表页链接）。
 * 模块可注册自己的任务给 #/map?draw=<id> 用（公交的线路走向、站点选点就是），
 * 工厂收到 URLSearchParams，返回：
 *   { type: "point"|"line"|"polygon", hint, minPoints, onFinish(geometry, mode) }
 * onFinish 拿到的是画好的几何（线不闭合、面自动闭合），由任务自己提交并 dataChanged。 */
const drawTasks = {};
function registerDrawTask(id, factory) { drawTasks[id] = factory; }

async function loadLayerCache() {
  if (layerCache) return layerCache;
  layerCache = Object.fromEntries(
    await Promise.all(mapPlugins.map(async p => [p.id, await p.load()])));
  return layerCache;
}

/* ---- 内置图层（注册顺序即叠放顺序，从下往上） ---- */

registerMapPlugin({
  id: "edges", label: "路网", color: "#64748B", on: true,
  load: async () => (await geoOnce("graph", "/api/v1/admin/graph")).features
    .filter(f => f.properties.layer === "edge"),
  layers: [{ id: "l-edges", type: "line",
    paint: { "line-color": ["case", ["==", ["get", "edge_kind"], "stair"], "#B45309", "#3B6EA5"],
             "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1, 17, 2.4],
             "line-opacity": .75 } }],
});

registerMapPlugin({
  id: "buildings", label: "建筑轮廓", color: "#1E40AF", on: true,
  load: async () => (await geoOnce("buildings", "/api/v1/admin/buildings/geometry")).features
    .filter(f => f.properties.layer === "building"),
  layers: [
    { id: "l-buildings-fill", type: "fill",
      paint: { "fill-color": "#2563EB", "fill-opacity": ["case", ["get", "has_indoor_map"], .42, .22] } },
    { id: "l-buildings-line", type: "line",
      paint: { "line-color": "#1E40AF", "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1, 17, 2] } },
  ],
  focusId: f => f.properties.building_id,
  popup: f => {
    const p = f.properties;
    const height = p.height_m ? `${Number(p.height_m).toFixed(1)} m${p.height_source === "estimated_floors" ? "（估）" : ""}` : "—";
    return `
      <h4>${esc(p.name)}</h4>
      <div class="kv">ID ${esc(p.building_id)}</div>
      <div class="kv">高度 ${esc(height)} · ${p.has_indoor_map ? "有室内图" : "无室内图"}</div>
      <div class="popup-actions">
        <a class="btn btn-sm" href="#/buildings?edit=${encodeURIComponent(p.building_id)}">编辑档案</a>
        <a class="btn btn-sm" href="#/map?redraw=${encodeURIComponent(p.building_id)}">重画轮廓</a>
        <button class="btn btn-sm btn-danger" data-act="remove" type="button">删除</button>
      </div>`;
  },
  remove: f => {
    const id = f.properties.building_id;
    confirmDialog("删除建筑", `确定删除建筑 <b>${esc(id)}</b> 吗？其楼层、室内要素、出入口与内部路网节点将一并删除。`,
      async () => {
        await api(`/api/v1/admin/buildings/${encodeURIComponent(id)}`, { method: "DELETE" });
        toast("建筑已删除");
        dataChanged("buildings");
      });
  },
});

registerMapPlugin({
  id: "nodes", label: "路网节点", color: "#94A3B8", on: false,
  load: async () => (await geoOnce("graph", "/api/v1/admin/graph")).features
    .filter(f => f.properties.layer === "node"),
  layers: [{ id: "l-nodes", type: "circle",
    paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 14, 1.5, 18, 4],
             "circle-color": "#94A3B8", "circle-stroke-width": .5, "circle-stroke-color": "#fff" } }],
});

registerMapPlugin({
  id: "entrances", label: "出入口", color: "#15803D", on: true,
  load: async () => (await geoOnce("buildings", "/api/v1/admin/buildings/geometry")).features
    .filter(f => f.properties.layer === "entrance"),
  layers: [{ id: "l-entrances", type: "circle",
    paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 2.5, 18, 6],
             "circle-color": "#15803D", "circle-stroke-width": 1.5, "circle-stroke-color": "#fff" } }],
  popup: f => {
    const p = f.properties;
    return `
      <h4>${esc(p.name || "未命名出入口")}</h4>
      <div class="kv">${p.is_accessible ? "无障碍" : "有台阶/不可无障碍"}${p.building_id ? " · " + esc(p.building_id) : ""}</div>
      <div class="popup-actions">
        ${p.building_id ? `<a class="btn btn-sm" href="#/buildings?edit=${encodeURIComponent(p.building_id)}">所属建筑</a>` : ""}
        ${p.building_id ? `<a class="btn btn-sm" href="#/map?focus=${encodeURIComponent(p.building_id)}">定位建筑</a>` : ""}
      </div>`;
  },
});

registerMapPlugin({
  id: "pois", label: "地点 POI", color: "#D97706", on: true,
  load: async () => (await geoOnce("pois", "/api/v1/admin/pois/geometry")).features,
  layers: [{ id: "l-pois", type: "circle",
    paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 3, 18, 7],
             "circle-color": "#D97706", "circle-stroke-width": 1.5, "circle-stroke-color": "#fff" } }],
  focusId: f => String(f.properties.poi_id),
  popup: f => {
    const p = f.properties;
    return `
      <h4>${esc(p.name)}</h4>
      <div class="kv">${esc(p.category || "未分类")}${p.building_id ? " · " + esc(p.building_id) : ""}</div>
      <div class="popup-actions">
        <a class="btn btn-sm" href="#/pois?edit=${p.poi_id}">编辑地点</a>
        <button class="btn btn-sm btn-danger" data-act="remove" type="button">删除</button>
      </div>`;
  },
  remove: f => {
    const id = Number(f.properties.poi_id);
    confirmDialog("删除地点", "确定删除这个地点吗？", async () => {
      await api(`/api/v1/admin/pois/${id}`, { method: "DELETE" });
      toast("地点已删除");
      dataChanged("pois");
    });
  },
});

registerMapPlugin({
  id: "features", label: "通用地物", color: "#0E7490", on: true,
  load: async () => (await geoOnce("features", "/api/v1/admin/features/geometry")).features,
  layers: [
    { id: "l-features-fill", type: "fill",
      filter: ["match", ["geometry-type"], ["Polygon", "MultiPolygon"], true, false],
      paint: { "fill-color": "#0E7490", "fill-opacity": .3 } },
    { id: "l-features-line", type: "line",
      filter: ["match", ["geometry-type"], ["LineString", "MultiLineString", "Polygon", "MultiPolygon"], true, false],
      paint: { "line-color": "#0E7490", "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1.2, 17, 2.6] } },
    { id: "l-features-point", type: "circle",
      filter: ["==", ["geometry-type"], "Point"],
      paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 3.5, 18, 7],
               "circle-color": "#0E7490", "circle-stroke-width": 1.5, "circle-stroke-color": "#fff" } },
  ],
  focusId: f => String(f.properties.feature_id),
  popup: f => {
    const p = f.properties;
    return `
      <h4>${esc(p.name)}</h4>
      <div class="kv">${esc(KIND_LABEL[p.kind] || p.kind)}${p.status === "draft" ? " · 草稿" : ""}</div>
      <div class="popup-actions">
        <a class="btn btn-sm" href="#/features?edit=${p.feature_id}">编辑地物</a>
        <button class="btn btn-sm btn-danger" data-act="remove" type="button">删除</button>
      </div>`;
  },
  remove: f => {
    const id = Number(f.properties.feature_id);
    confirmDialog("删除地物", "确定删除这个地物吗？App 端将不再显示它。", async () => {
      await api(`/api/v1/admin/features/${id}`, { method: "DELETE" });
      toast("地物已删除");
      dataChanged("features");
    });
  },
});

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
          <div class="map-add-grid">
            <button class="btn btn-sm" id="btn-add-poi" type="button">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>
              地点
            </button>
            <button class="btn btn-sm" id="btn-add-point" type="button">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></svg>
              点地物
            </button>
            <button class="btn btn-sm" id="btn-add-polygon" type="button">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12 3 21 9 17.5 20 6.5 20 3 9"/></svg>
              面地物
            </button>
            <button class="btn btn-sm" id="btn-add-building" type="button">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="2" width="16" height="20" rx="1"/><line x1="9" y1="7" x2="10" y2="7"/><line x1="14" y1="7" x2="15" y2="7"/><path d="M10 22v-4a2 2 0 0 1 4 0v4"/></svg>
              建筑轮廓
            </button>
          </div>
          <div class="map-add-hint" id="add-hint" hidden></div>
        </div>
        <p class="legend-note">点击建筑/地物查看详情；高度为估算值的以“估”标注。<br>提交人由管理令牌的名字部分记录（见左下角令牌设置）。<br>瓦片与样式自托管 · 数据 © OpenStreetMap contributors</p>
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
    layers = await loadLayerCache();
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

  /* 业务数据图层：setStyle 会清空所有源/图层，切换底图后调用本函数重挂。
   * 源与层完全由 mapPlugins 驱动——加一类图层只需注册插件，不必改这里。 */
  function addDataLayers() {
    if (map.getLayer("l-draft-fill")) return;
    for (const p of mapPlugins) {
      map.addSource(`s-${p.id}`, { type: "geojson", data: fc(layers[p.id] || []) });
      for (const def of p.layers) {
        map.addLayer({ ...def, source: `s-${p.id}` });
        if (!p.on) map.setLayoutProperty(def.id, "visibility", "none");
      }
    }
    map.addSource("s-highlight", { type: "geojson", data: fc([]) });
    map.addLayer({ id: "l-highlight", type: "line", source: "s-highlight",
      paint: { "line-color": "#D97706", "line-width": 3 } });
    // 绘制草稿：面、线、顶点分开三层，最后添加保证在最上层
    map.addSource("s-draft-fill", { type: "geojson", data: fc([]) });
    map.addSource("s-draft-line", { type: "geojson", data: fc([]) });
    map.addSource("s-draft-vertex", { type: "geojson", data: fc([]) });
    map.addLayer({ id: "l-draft-fill", type: "fill", source: "s-draft-fill",
      paint: { "fill-color": "#D97706", "fill-opacity": .22 } });
    map.addLayer({ id: "l-draft-line", type: "line", source: "s-draft-line",
      paint: { "line-color": "#B45309", "line-width": 2, "line-dasharray": [2, 1] } });
    map.addLayer({ id: "l-draft-vertex", type: "circle", source: "s-draft-vertex",
      paint: { "circle-radius": 4.5, "circle-color": "#fff",
               "circle-stroke-width": 2, "circle-stroke-color": "#B45309" } });
  }
  addDataLayers();

  /* ---- 图层面板：底图 + 各插件（面板顺序见 LAYER_ORDER，表外的按注册顺序追加） ---- */
  const listEl = $("#layer-list");
  const orderedPlugins = [...mapPlugins].sort((a, b) => {
    const idx = id => { const i = LAYER_ORDER.indexOf(id); return i < 0 ? Number.MAX_SAFE_INTEGER : i; };
    return idx(a.id) - idx(b.id);
  });
  const pluginById = Object.fromEntries(mapPlugins.map(p => [p.id, p]));
  listEl.innerHTML = [BASEMAP_DEF, ...orderedPlugins].map(d => `
    <label class="layer-item"><input type="checkbox" data-layer="${d.id}" ${d.on ? "checked" : ""}>
      <span class="layer-swatch" style="background:${d.color}"></span>${esc(d.label)}</label>`).join("");
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

  listEl.addEventListener("change", e => {
    const key = e.target.dataset.layer;
    if (key === "basemap") { setBasemap(e.target.checked); return; }
    const p = pluginById[key];
    if (!p) return;
    p.on = e.target.checked;   // 开关状态记在插件上，切页面回来不丢
    for (const def of p.layers) {
      map.setLayoutProperty(def.id, "visibility", e.target.checked ? "visible" : "none");
    }
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
      const gj = await geoOnce("buildings", "/api/v1/admin/buildings/geometry");
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
  let popupOwner = null; // { plugin, feature }：弹窗里删除按钮要回它自己那一格
  // 绘制模式：{type:'poi'|'point'|'polygon'|'building'|'redraw'|'redraw-feature'|'line', ...}
  // 注册的绘制任务还会带 hint / minPoints / onFinish，见 registerDrawTask
  let addMode = null;
  let draft = []; // 正在绘制的顶点 [[lng,lat], ...]

  /* 就地刷新：dataChanged(kind) 会调到这里，只重取受影响的图层，不重建地图。 */
  mapPluginsLive = {};
  for (const p of mapPlugins) {
    mapPluginsLive[p.id] = async () => {
      const fresh = await p.load();
      layers[p.id] = fresh;
      if (layerCache) layerCache[p.id] = fresh; // 与模块级缓存保持同一份数据
      const src = map.getSource(`s-${p.id}`);
      if (src) src.setData(fc(fresh));
      if (p.id === "pois") refreshPoiLabels();
      if (p.id === "buildings") brightStyleCache = null; // 底图楼名层的数据已变，下次切换底图重建
    };
  }

  /* 弹窗里的删除按钮：动作由插件自己实现（含确认与 dataChanged） */
  function wirePopupActions() {
    const el = popup.getElement();
    const btn = el && el.querySelector("[data-act=remove]");
    if (!btn || !popupOwner || !popupOwner.plugin.remove) return;
    btn.addEventListener("click", () => {
      const { plugin, feature } = popupOwner;
      popup.remove();
      plugin.remove(feature);
    });
  }

  /* 点击/光标：有 popup 的插件才可点（原来出入口有手型光标却点不动） */
  for (const p of mapPlugins) {
    if (!p.popup) continue;
    for (const def of p.layers) {
      map.on("click", def.id, e => {
        if (addMode || !e.features.length) return;
        popupOwner = { plugin: p, feature: e.features[0] };
        popup.setLngLat(e.lngLat).setHTML(p.popup(e.features[0])).addTo(map);
        wirePopupActions();
      });
      map.on("mouseenter", def.id, () => { if (!addMode) map.getCanvas().style.cursor = "pointer"; });
      map.on("mouseleave", def.id, () => (map.getCanvas().style.cursor = ""));
    }
  }

  /* ---- 绘制模式：在地图上点选 / 画面后提交 ----
   * poi            新增地点（POI，单点）
   * point          新增点地物
   * polygon        新增面地物
   * building       新增建筑（画轮廓）
   * redraw         重画某栋建筑的轮廓
   * redraw-feature 重画某个地物的轮廓
   * line / 其它    由 registerDrawTask 注册的任务（如公交线路走向）
   * 画面时依次点击顶点，点回第一个顶点或按 Enter 收尾，Esc 取消。 */
  const modeButtons = {
    poi: $("#btn-add-poi"),
    point: $("#btn-add-point"),
    polygon: $("#btn-add-polygon"),
    building: $("#btn-add-building"),
  };
  const addHint = $("#add-hint");
  const MODE_HINT = {
    poi: "在地图上点击要放置的位置，Esc 取消",
    point: "在地图上点击要放置的位置，Esc 取消",
    polygon: "依次点击轮廓顶点；点回第一个顶点或按 Enter 完成，Esc 取消",
    building: "依次点击建筑轮廓顶点；点回第一个顶点或按 Enter 完成，Esc 取消",
    redraw: "重画建筑轮廓：依次点击顶点；点回第一个顶点或按 Enter 完成，Esc 取消",
    "redraw-feature": "重画地物轮廓：依次点击顶点；点回第一个顶点或按 Enter 完成，Esc 取消",
    line: "依次点击线路的拐点；按 Enter 或点回最后一个点完成，Esc 取消",
  };
  const minPointsOf = mode => mode?.minPoints ?? (mode?.type === "line" ? 2 : 3);

  /* 草稿几何：面 / 线 / 顶点各一个源——图层类型不同，无法共用一个源。
   * 线模式不闭合（否则画出来的走向会多一条收尾段）。 */
  function updateDraft() {
    const closed = addMode?.type !== "line";
    const ring = closed && draft.length >= 3 ? [...draft, draft[0]] : null;
    const line = map.getSource("s-draft-line");
    const fill = map.getSource("s-draft-fill");
    const vertex = map.getSource("s-draft-vertex");
    if (line) line.setData(fc(draft.length >= 2
      ? [{ type: "Feature", geometry: { type: "LineString", coordinates: ring || draft }, properties: {} }]
      : []));
    if (fill) fill.setData(fc(ring
      ? [{ type: "Feature", geometry: { type: "Polygon", coordinates: [ring] }, properties: {} }]
      : []));
    if (vertex) vertex.setData(fc(draft.map(p => ({
      type: "Feature", geometry: { type: "Point", coordinates: p }, properties: {},
    }))));
  }

  function startDraw(mode) {
    addMode = mode;
    draft = [];
    for (const [key, btn] of Object.entries(modeButtons)) btn.classList.toggle("active", key === mode.type);
    addHint.hidden = false;
    addHint.textContent = mode.hint || MODE_HINT[mode.type] || "";
    map.getCanvas().style.cursor = "crosshair";
    updateDraft();
  }
  function stopDraw() {
    addMode = null;
    draft = [];
    for (const btn of Object.values(modeButtons)) btn.classList.remove("active");
    addHint.hidden = true;
    map.getCanvas().style.cursor = "";
    updateDraft();
  }
  for (const [key, btn] of Object.entries(modeButtons)) {
    btn.addEventListener("click", () => {
      if (addMode && addMode.type === key) stopDraw();
      else startDraw({ type: key });
    });
  }

  document.addEventListener("keydown", function drawKeys(e) {
    if (mapCleanup === null) { document.removeEventListener("keydown", drawKeys); return; }
    if (!addMode) return;
    if (e.key === "Escape") { stopDraw(); return; }
    if (e.key === "Enter" && draft.length >= minPointsOf(addMode)) { e.preventDefault(); finishDraw(); }
  });

  /* 收尾：把草稿顶点变成几何，交给对应的提交流程 */
  function finishDraw() {
    const mode = addMode, vertices = draft.slice();
    const min = minPointsOf(mode);
    stopDraw();
    if (!mode) return;
    if (vertices.length < min) { toast(`至少需要 ${min} 个顶点`, "err"); return; }
    const geometry = mode.type === "line"
      ? { type: "LineString", coordinates: vertices }
      : { type: "Polygon", coordinates: [[...vertices, vertices[0]]] };
    if (mode.onFinish) { mode.onFinish(geometry, mode); return; }   // 注册的绘制任务自己提交
    if (mode.type === "building") { openCreateBuildingDialog(geometry, vertices.length); return; }
    if (mode.type === "redraw") {
      confirmDialog("重画建筑轮廓",
        `确定用新画的轮廓（${vertices.length} 个顶点）替换建筑 <b>${esc(mode.buildingId)}</b> 的原轮廓吗？`,
        async () => {
          await api(`/api/v1/admin/buildings/${encodeURIComponent(mode.buildingId)}/geometry`, {
            method: "PATCH", body: { geometry },
          });
          toast("轮廓已更新");
          dataChanged("buildings");
        }, "替换");
      return;
    }
    if (mode.type === "redraw-feature") {
      confirmDialog("重画地物轮廓",
        `确定用新画的轮廓（${vertices.length} 个顶点）替换地物 <b>#${mode.featureId}</b> 的原几何吗？`,
        async () => {
          await api(`/api/v1/admin/features/${mode.featureId}`, { method: "PATCH", body: { geometry } });
          toast("地物几何已更新");
          dataChanged("features");
        }, "替换");
      return;
    }
    openFeatureDialog(geometry, `面地物 · ${vertices.length} 个顶点`);
  }

  map.on("click", e => {
    if (!addMode) return;
    const mode = addMode;
    const { lng, lat } = e.lngLat;
    if (mode.type === "poi") { stopDraw(); openCreatePOIDialog(lng, lat); return; }
    if (mode.type === "point") {
      stopDraw();
      if (mode.onFinish) { mode.onFinish({ type: "Point", coordinates: [lng, lat] }, mode); return; }
      openFeatureDialog({ type: "Point", coordinates: [lng, lat] },
        `点地物 · ${lng.toFixed(5)}, ${lat.toFixed(5)}`);
      return;
    }
    // 画面/画线：点回已画的端点即收尾（屏幕距离 12px 内视为同一点）
    const anchor = mode.type === "line" ? draft[draft.length - 1] : draft[0];
    if (anchor && draft.length >= minPointsOf(mode)) {
      const a = map.project(anchor), here = map.project(e.lngLat);
      if (Math.hypot(a.x - here.x, a.y - here.y) < 12) { finishDraw(); return; }
    }
    draft.push([lng, lat]);
    updateDraft();
  });

  function openCreatePOIDialog(lng, lat) {
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
      dataChanged("pois");
    });
  }

  /* 定位：#/map?focus=<id>（按建筑，兼容旧链接与搜索框）或
   * #/map?focus=<pluginId>:<id>（按插件定位，公交线路/站点用）。 */
  function focusFeature(spec) {
    const raw = String(spec);
    const sep = raw.indexOf(":");
    const pluginId = sep > 0 ? raw.slice(0, sep) : "buildings";
    const id = sep > 0 ? raw.slice(sep + 1) : raw;
    const p = mapPlugins.find(x => x.id === pluginId);
    if (!p || !p.focusId) { toast("该图层不支持定位", "err"); return; }
    const feats = (layers[p.id] || []).filter(f =>
      String(p.focusId(f)) === id || (p.id === "buildings" && f.properties.name === id));
    if (!feats.length) { toast("未找到目标", "err"); return; }
    const coords = feats[0].geometry.coordinates.flat(2);
    const lngs = coords.map(c => c[0]), lats = coords.map(c => c[1]);
    map.fitBounds([[Math.min(...lngs), Math.min(...lats)], [Math.max(...lngs), Math.max(...lats)]],
      { padding: 90, maxZoom: 17.5, duration: 700 });
    const src = map.getSource("s-highlight");
    if (src) src.setData(fc(feats));
  }
  $("#map-q").addEventListener("keydown", e => {
    if (e.key === "Enter") { e.preventDefault(); const v = e.target.value.trim(); if (v) focusFeature(v); }
  });

  if (params.get("focus")) focusFeature(params.get("focus"));
  // 从列表页过来的绘制入口：#/map?draw=polygon / #/map?redraw=<buildingId> /
  // #/map?redraw-feature=<id>，以及模块注册的任务（#/map?draw=bus-route&route=1）
  const drawParam = params.get("draw");
  if (drawParam && modeButtons[drawParam]) startDraw({ type: drawParam });
  else if (drawParam && drawTasks[drawParam]) {
    // 任务工厂可以是异步的（要先取线路/站点档案才能写出提示语）
    Promise.resolve(drawTasks[drawParam](params))
      .then(mode => { if (mode && mapCleanup) startDraw(mode); }) // 等待期间已切走就别再画了
      .catch(err => toast("进入绘制失败：" + err.message, "err"));
  }
  const redrawBuilding = params.get("redraw");
  if (redrawBuilding) startDraw({ type: "redraw", buildingId: redrawBuilding });
  const redrawFeature = params.get("redraw-feature");
  if (redrawFeature) startDraw({ type: "redraw-feature", featureId: Number(redrawFeature) });
  refreshPoiLabels();

  /* 轮询图层（实时车辆）：挂载期间定时就地刷新；页面隐藏时跳过，切回来下一拍自动补上 */
  const pollTimers = [];
  for (const p of mapPlugins) {
    if (!p.refreshMs) continue;
    pollTimers.push(setInterval(() => {
      if (document.hidden || !p.on) return;
      mapPluginsLive[p.id]?.().catch(() => { /* 单次轮询失败不打断地图 */ });
    }, p.refreshMs));
  }

  mapCleanup = () => {
    popup.remove();
    poiMarkers.forEach(m => m.remove());
    pollTimers.forEach(clearInterval);
    mapPluginsLive = {}; // 未挂载时 dataChanged 只清缓存，下次进地图重新拉
    map.remove();
  };
}

/* ---------------- 视图：建筑 ---------------- */

async function loadBuildings(force = false) {
  if (force) listCaches.buildings = null;
  if (!listCaches.buildings) {
    const d = await api("/api/v1/buildings");
    listCaches.buildings = d.buildings || [];
  }
  return listCaches.buildings;
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
      <div class="head-actions">
        <a class="btn btn-primary" href="#/map?draw=building">地图上画轮廓新建</a>
      </div>
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

  /* 增删改后统一走 dataChanged：清列表缓存、失效地图图层、让已挂载的地图就地重取 */
  const reload = async () => {
    dataChanged("buildings");
    buildings = await loadBuildings();
    render();
  };

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
        await reload();
      });
    } else {
      const b = buildings.find(x => x.building_id === id);
      if (b) editBuildingDialog(b, reload);
    }
  });

  render();
  const editId = params.get("edit");
  if (editId) {
    const b = buildings.find(x => x.building_id === editId);
    if (b) editBuildingDialog(b, reload);
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

async function loadPOIs(force = false) {
  if (force) listCaches.pois = null;
  if (!listCaches.pois) {
    const d = await api("/api/v1/pois?limit=100");
    let list = d.pois || [];
    if (list.length < d.count) { // 超过 100 条时走管理端全量接口
      // 该接口不带 keywords：标成 null 让编辑弹窗不要覆盖已有搜索词
      list = (await geoOnce("pois", "/api/v1/admin/pois/geometry")).features
        .map(f => ({ poi_id: f.properties.poi_id, name: f.properties.name,
                     category: f.properties.category, building_id: f.properties.building_id,
                     keywords: null, lng: f.geometry.coordinates[0], lat: f.geometry.coordinates[1] }));
    }
    listCaches.pois = list;
  }
  return listCaches.pois;
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

  const reload = async () => {
    dataChanged("pois");
    pois = await loadPOIs();
    render();
  };

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
        await reload();
      });
    } else {
      editPOIDialog(p, reload);
    }
  });

  render();
  const editId = params.get("edit");
  if (editId) {
    const p = pois.find(x => String(x.poi_id) === editId);
    if (p) editPOIDialog(p, reload);
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
    const body = { name, category: cat, clear_category: !cat };
    // keywords 为 null 表示这份数据来自全量回退接口（没有关键词字段），
    // 此时不发 keywords —— PATCH 缺字段即"不改"，避免把原有搜索词清空
    if (p.keywords) body.keywords = kwRaw ? kwRaw.split(/[,，、\s]+/).filter(Boolean) : [];
    await api(`/api/v1/admin/pois/${p.poi_id}`, { method: "PATCH", body });
    toast("地点已保存");
    await onSaved?.();
  });
}

/* ---------------- 视图：通用地物 ---------------- */

/* 地物类别白名单，与后端 migrations/000004_map_features.up.sql 的 CHECK 一致 */
const FEATURE_KINDS = [
  ["road", "道路"], ["path", "小径"], ["green", "绿地"], ["water", "水系"],
  ["square", "广场"], ["sports", "运动场地"], ["gate", "校门/出入口"],
  ["bus_stop", "公交站"], ["parking", "停车点"], ["food", "餐饮"], ["shop", "商店"],
  ["study", "学习场所"], ["service", "服务设施"], ["sculpture", "景观雕塑"],
  ["facility", "设施"], ["other", "其他"],
];
const KIND_LABEL = Object.fromEntries(FEATURE_KINDS);
const GEOM_LABEL = { Point: "点", MultiPoint: "多点", LineString: "线", MultiLineString: "多线",
                     Polygon: "面", MultiPolygon: "多面" };

async function loadFeatures(force = false) {
  if (force) listCaches.features = null;
  if (!listCaches.features) {
    const d = await api("/api/v1/admin/features");
    listCaches.features = d.features || [];
  }
  return listCaches.features;
}

function kindOptions(selected) {
  return FEATURE_KINDS.map(([v, label]) =>
    `<option value="${v}"${v === selected ? " selected" : ""}>${label}</option>`).join("");
}

/** 提交新地物：类别 + 名称 + 说明 + 状态，几何在调用前已确定。 */
function openFeatureDialog(geometry, subtitle) {
  openDialog("新建地物", `
    <div class="field"><label>几何</label><div class="cell-sub">${esc(subtitle)}</div></div>
    <div class="field"><label for="nf-kind">类别 <span class="hint">（必填）</span></label>
      <select id="nf-kind">${kindOptions("green")}</select></div>
    <div class="field"><label for="nf-name">名称 <span class="hint">（必填）</span></label>
      <input type="text" id="nf-name" placeholder="如: 明德楼西侧绿地" required><p class="field-error"></p></div>
    <div class="field"><label class="check-row"><input type="checkbox" id="nf-showname" checked> 在地图上标注名称</label>
      <p class="cell-sub">绿地、水面这类面建议不标：名字会盖住地图，但列表与详情页照常显示。</p></div>
    <div class="field"><label for="nf-desc">说明 <span class="hint">（可留空，App 详情页会显示）</span></label>
      <textarea id="nf-desc" rows="3" placeholder="来历、用途、开放时间等"></textarea></div>
    <div class="field"><label for="nf-status">状态</label>
      <select id="nf-status">
        <option value="published">发布（App 立即可见）</option>
        <option value="draft">草稿（仅管理台可见）</option>
      </select></div>`, async () => {
    const name = $("#nf-name").value.trim();
    if (!name) throw new Error("名称不能为空");
    const d = await api("/api/v1/admin/features", {
      method: "POST",
      body: {
        kind: $("#nf-kind").value,
        name,
        description: $("#nf-desc").value.trim(),
        show_name: $("#nf-showname").checked,
        status: $("#nf-status").value,
        geometry,
      },
    });
    toast(`地物已提交（#${d.feature_id}）`);
    dataChanged("features");
  });
}

/** 新建建筑：画好轮廓后填档案字段。 */
function openCreateBuildingDialog(geometry, vertexCount) {
  openDialog("新建建筑", `
    <div class="field"><label>轮廓</label><div class="cell-sub">已绘制 ${vertexCount} 个顶点</div></div>
    <div class="field"><label for="nb-name">名称 <span class="hint">（必填）</span></label>
      <input type="text" id="nb-name" placeholder="如: 文德楼" required><p class="field-error"></p></div>
    <div class="field"><label for="nb-id">建筑编号 <span class="hint">（可留空，自动生成 ADM-xxxxxxxx）</span></label>
      <input type="text" id="nb-id" placeholder="留空自动生成" style="font-family:var(--mono)"></div>
    <div class="field"><label for="nb-h">高度（米）</label>
      <input type="number" id="nb-h" step="0.1" min="0" placeholder="可留空"><p class="field-error"></p></div>
    <div class="field"><label for="nb-hs">高度来源 <span class="hint">（填了高度就必须选）</span></label>
      <select id="nb-hs">
        <option value="">—</option>
        <option value="measured">measured · 实测</option>
        <option value="estimated_floors">estimated_floors · 按楼层估算</option>
        <option value="display_default">display_default · 展示默认值</option>
      </select></div>
    <p class="cell-sub">新建建筑只有档案与轮廓；楼层、室内要素、出入口仍走 QGIS / 脚本导入。</p>`, async () => {
    const name = $("#nb-name").value.trim();
    if (!name) throw new Error("名称不能为空");
    const rawHeight = $("#nb-h").value.trim();
    const height = rawHeight === "" ? null : Number(rawHeight);
    if (height !== null && !Number.isFinite(height)) throw new Error("高度应为数字");
    if (height !== null && !$("#nb-hs").value) throw new Error("填写高度时必须选择高度来源");
    const d = await api("/api/v1/admin/buildings", {
      method: "POST",
      body: {
        building_id: $("#nb-id").value.trim(),
        name,
        height_m: height,
        height_source: $("#nb-hs").value,
        geometry,
      },
    });
    toast(`建筑已创建（${d.building_id}）`);
    dataChanged("buildings");
  });
}

async function viewFeatures(root, params) {
  root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
  let features;
  try {
    features = await loadFeatures();
  } catch (err) {
    root.innerHTML = `<div class="empty"><p>地物加载失败：${esc(err.message)}</p></div>`;
    return;
  }

  let keyword = "", kind = "", status = "", nameVis = "";
  root.innerHTML = `
    <div class="page-head">
      <div><h1>地物管理</h1><div class="sub" id="f-sub">${features.length} 个通用地物（建筑与地点之外的道路、绿地、广场等）</div></div>
      <div class="head-actions">
        <a class="btn" href="#/map?draw=point">地图上画点</a>
        <a class="btn btn-primary" href="#/map?draw=polygon">地图上画面</a>
      </div>
    </div>
    <div class="toolbar">
      <div class="search-box">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
        <input type="text" id="f-q" placeholder="按名称 / 说明过滤…" aria-label="过滤地物">
      </div>
      <select id="f-kind" aria-label="按类别过滤"><option value="">全部类别</option>${kindOptions("")}</select>
      <select id="f-status" aria-label="按状态过滤">
        <option value="">全部状态</option><option value="published">已发布</option><option value="draft">草稿</option>
      </select>
      <select id="f-showname" aria-label="按名称标注过滤">
        <option value="">名称标注（全部）</option>
        <option value="yes">标注名称</option>
        <option value="no">不标注名称</option>
      </select>
      <span class="result-count" id="f-count"></span>
    </div>
    <div class="card table-wrap" style="max-height:calc(100vh - 210px)">
      <table aria-label="地物列表">
        <thead><tr><th>ID</th><th>名称</th><th>类别</th><th>状态</th><th>名称标注</th><th>几何</th><th>提交人</th><th>更新时间</th><th></th></tr></thead>
        <tbody id="f-body"></tbody>
      </table>
      <div class="empty" id="f-empty" hidden><p>没有匹配的地物</p></div>
    </div>
    <div class="empty" id="f-none" hidden>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><polygon points="12 3 21 9 17.5 20 6.5 20 3 9"/></svg>
      <p>还没有任何通用地物 —— 去 <a href="#/map?draw=polygon">地图总览</a> 画面提交，或 <a href="#/map?draw=point">画个点</a></p>
    </div>`;

  function render() {
    const kw = keyword.trim().toLowerCase();
    const filtered = features.filter(f =>
      (!kw || (f.name || "").toLowerCase().includes(kw) || (f.description || "").toLowerCase().includes(kw)) &&
      (!kind || f.kind === kind) && (!status || f.status === status) &&
      (!nameVis || (nameVis === "yes") === !!f.show_name));
    $("#f-none").hidden = features.length > 0;
    $("#f-body").innerHTML = filtered.map(f => `
      <tr data-id="${f.feature_id}">
        <td class="cell-id">${f.feature_id}</td>
        <td><strong>${esc(f.name)}</strong>${f.description ? `<div class="cell-sub">${esc(f.description)}</div>` : ""}</td>
        <td><span class="badge blue">${esc(KIND_LABEL[f.kind] || f.kind)}</span></td>
        <td>${f.status === "draft" ? `<span class="badge warn">草稿</span>` : `<span class="badge ok">已发布</span>`}</td>
        <td>${f.show_name ? `<span class="badge">标注</span>` : `<span class="badge warn">不标注</span>`}</td>
        <td>${esc(GEOM_LABEL[(f.geometry && f.geometry.type) || ""] || "—")}</td>
        <td class="cell-id">${esc(f.created_by || "—")}</td>
        <td class="cell-id">${esc((f.updated_at || "").slice(0, 16).replace("T", " "))}</td>
        <td><div class="row-actions">
          <button class="icon-btn" data-act="edit" title="编辑地物" aria-label="编辑 ${esc(f.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
          </button>
          <a class="icon-btn" href="#/map?redraw-feature=${f.feature_id}" title="重画几何"
             aria-label="重画 ${esc(f.name)} 的几何" style="text-decoration:none">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12 3 21 9 17.5 20 6.5 20 3 9"/><circle cx="12" cy="3" r="1.4"/></svg>
          </a>
          <button class="icon-btn" data-act="del" title="删除地物" aria-label="删除 ${esc(f.name)}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
          </button>
        </div></td>
      </tr>`).join("");
    $("#f-empty").hidden = filtered.length > 0;
    $("#f-count").textContent = `共 ${filtered.length} 条`;
    $("#f-sub").textContent = `${features.length} 个通用地物（建筑与地点之外的道路、绿地、广场等）`;
  }

  const reload = async () => {
    dataChanged("features");
    features = await loadFeatures();
    render();
  };

  $("#f-q").addEventListener("input", e => { keyword = e.target.value; render(); });
  $("#f-kind").addEventListener("change", e => { kind = e.target.value; render(); });
  $("#f-status").addEventListener("change", e => { status = e.target.value; render(); });
  $("#f-showname").addEventListener("change", e => { nameVis = e.target.value; render(); });
  $("#f-body").addEventListener("click", e => {
    const btn = e.target.closest("button[data-act]");
    if (!btn) return;
    const id = Number(btn.closest("tr").dataset.id);
    const f = features.find(x => x.feature_id === id);
    if (!f) return;
    if (btn.dataset.act === "del") {
      confirmDialog("删除地物", `确定删除地物 <b>${esc(f.name)}</b> 吗？App 端将不再显示它。`, async () => {
        await api(`/api/v1/admin/features/${f.feature_id}`, { method: "DELETE" });
        toast("地物已删除");
        await reload();
      });
    } else {
      editFeatureDialog(f, reload);
    }
  });

  render();
  const editId = params.get("edit");
  if (editId) {
    const f = features.find(x => String(x.feature_id) === editId);
    if (f) editFeatureDialog(f, reload);
  }
}

function editFeatureDialog(f, onSaved) {
  openDialog(`编辑地物 · ${f.name}`, `
    <div class="field"><label for="ef-kind">类别</label>
      <select id="ef-kind">${kindOptions(f.kind)}</select></div>
    <div class="field"><label for="ef-name">名称 <span class="hint">（必填）</span></label>
      <input type="text" id="ef-name" value="${esc(f.name)}" required><p class="field-error"></p></div>
    <div class="field"><label class="check-row"><input type="checkbox" id="ef-showname" ${f.show_name ? "checked" : ""}> 在地图上标注名称</label>
      <p class="cell-sub">关掉只影响地图上的文字标签：列表、搜索与详情页照旧显示名称。</p></div>
    <div class="field"><label for="ef-desc">说明</label>
      <textarea id="ef-desc" rows="3">${esc(f.description || "")}</textarea></div>
    <div class="field"><label for="ef-status">状态</label>
      <select id="ef-status">
        <option value="published"${f.status === "published" ? " selected" : ""}>发布（App 可见）</option>
        <option value="draft"${f.status === "draft" ? " selected" : ""}>草稿（仅管理台）</option>
      </select></div>
    <p class="cell-sub">几何：${esc(GEOM_LABEL[(f.geometry && f.geometry.type) || ""] || "—")} —— 改形状请回地图用「重画几何」。</p>`, async () => {
    const name = $("#ef-name").value.trim();
    if (!name) throw new Error("名称不能为空");
    await api(`/api/v1/admin/features/${f.feature_id}`, {
      method: "PATCH",
      body: {
        kind: $("#ef-kind").value,
        name,
        description: $("#ef-desc").value.trim(),
        show_name: $("#ef-showname").checked,
        status: $("#ef-status").value,
      },
    });
    toast("地物已保存");
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

/* ---------------- 扩展接口（AdminKit） ----------------
 * 独立模块文件在 app.js 之后加载（见 index.html 的 script 顺序），通过 window.AdminKit
 * 拿框架能力；模块只用这里列出的 API，其余内部实现随时可以重构。
 * 注册视图后若当前 URL 已指向该视图，需要调一次 reroute（启动时 app.js 还不认识它）。 */
window.AdminKit = {
  // 基础
  $, $$, esc, fmtNum, toast, api, geojson, geoOnce,
  // 弹窗
  openDialog, confirmDialog,
  // 数据失效：写操作成功后必调，参数是 dataKey（buildings / pois / features / bus / all）
  dataChanged, onDataChanged,
  // 扩展点
  registerView, registerMapPlugin, registerDrawTask,
  reroute: route,
  // 常量
  KIND_LABEL, GEOM_LABEL,
};

/* ---------------- 启动 ---------------- */

route();
