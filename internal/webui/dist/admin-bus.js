/* 校园公交管理模块 —— 线路 / 站点 / 车辆三张表，以及地图上的公交图层与绘制任务。
 *
 * 这是「管理台扩展模块」的范例：app.js 里没有一行公交代码，本文件通过
 * window.AdminKit 注册三样东西（都在文件末尾）：
 *   1. registerView("bus", view)              —— #/bus 页面
 *   2. registerMapPlugin({...}) × 3           —— 地图图层：公交线路 / 站点 / 实时车辆
 *   3. registerDrawTask("bus-route"|"bus-stop") —— 在地图上画走向、点选站点位置
 * 三个图层共用 dataKey="bus"：改完数据只需要 dataChanged("bus")。
 *
 * 后端契约见 README「公交数据约定」与 internal/modules/bus。
 */
"use strict";
window.CampusBus = (() => {
  const K = window.AdminKit;
  const { esc, toast, api, openDialog, confirmDialog, dataChanged, geoOnce } = K;

  const DEFAULT_COLOR = "#0B7285"; // 与底图样式片段 configs/style.bus-layers.json 的默认线色一致
  const STATUS_BADGE = { published: '<span class="badge ok">已发布</span>', draft: '<span class="badge warn">草稿</span>' };
  const FRESH_SECONDS = 120; // 与后端 /bus/vehicles 默认 max_age_s 一致

  /* ---------------- 取数与缓存 ---------------- */
  /* 与 app.js 的 listCaches 同理，只是这一份归本模块自己管：
   * dataChanged("bus") 会清空它，下次进页面重新拉。 */
  const cache = { routes: null, stops: null, vehicles: null };
  K.onDataChanged(hit => {
    if (hit("bus")) { cache.routes = cache.stops = cache.vehicles = null; }
  });

  const loadRoutes = async () => {
    if (!cache.routes) cache.routes = (await api("/api/v1/admin/bus/routes?geometry=0")).routes || [];
    return cache.routes;
  };
  const loadStops = async () => {
    if (!cache.stops) cache.stops = (await api("/api/v1/admin/bus/stops")).stops || [];
    return cache.stops;
  };
  const loadVehicles = async () => {
    if (!cache.vehicles) cache.vehicles = (await api("/api/v1/admin/bus/vehicles")).vehicles || [];
    return cache.vehicles;
  };
  const loadRouteDetail = routeId => api(`/api/v1/admin/bus/routes/${routeId}`);

  /* ---------------- 小工具 ---------------- */
  const ICON = {
    add: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/></svg>',
    edit: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>',
    trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>',
    locate: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/></svg>',
    move: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="7"/><line x1="12" y1="1" x2="12" y2="5"/><line x1="12" y1="19" x2="12" y2="23"/><line x1="1" y1="12" x2="5" y2="12"/><line x1="19" y1="12" x2="23" y2="12"/></svg>',
    order: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="9" y1="6" x2="21" y2="6"/><line x1="9" y1="12" x2="21" y2="12"/><line x1="9" y1="18" x2="21" y2="18"/><circle cx="4" cy="6" r="1.4"/><circle cx="4" cy="12" r="1.4"/><circle cx="4" cy="18" r="1.4"/></svg>',
    path: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 18c7 0 7-12 14-12"/><circle cx="4.5" cy="18" r="2"/><circle cx="19.5" cy="6" r="2"/></svg>',
    power: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v9"/><path d="M6.6 6.6a8 8 0 1 0 10.8 0"/></svg>',
  };
  const iconBtn = (icon, act, title, extra = "") =>
    `<button class="icon-btn" data-act="${act}" title="${esc(title)}" aria-label="${esc(title)}" type="button" ${extra}>${icon}</button>`;
  const ts = value => esc(String(value || "").slice(0, 16).replace("T", " ")) || "—";
  const statusOptions = current => `
    <option value="published"${current !== "draft" ? " selected" : ""}>发布（App 立即可见）</option>
    <option value="draft"${current === "draft" ? " selected" : ""}>草稿（仅管理台可见）</option>`;
  const routeLabel = r => `${r.code} ${r.name}`.trim();

  /* ---------------- 页面：#/bus ---------------- */

  async function view(root) {
    root.innerHTML = `<div class="loading"><span class="spinner"></span>加载中…</div>`;
    let routes, stops, vehicles;
    try {
      [routes, stops, vehicles] = await Promise.all([loadRoutes(), loadStops(), loadVehicles()]);
    } catch (err) {
      root.innerHTML = `<div class="empty"><p>公交数据加载失败：${esc(err.message)}</p></div>`;
      return;
    }

    root.innerHTML = `
      <div class="page-head">
        <div><h1>公交管理</h1><div class="sub" id="bus-sub"></div></div>
        <div class="head-actions">
          <a class="btn" href="#/map?draw=bus-stop">地图上新增站点</a>
          <button class="btn btn-primary" id="bus-new-route" type="button">${ICON.add} 新建线路</button>
        </div>
      </div>

      <h2 class="section-title">线路 <span class="hint">线路先建档（编号/名称/线色），再在地图上画走向、排站序</span></h2>
      <div class="toolbar">
        <div class="search-box">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input type="text" id="br-q" placeholder="按编号 / 名称过滤…" aria-label="过滤线路">
        </div>
        <select id="br-status" aria-label="按状态过滤">
          <option value="">全部状态</option><option value="published">已发布</option><option value="draft">草稿</option>
        </select>
        <span class="result-count" id="br-count"></span>
      </div>
      <div class="card table-wrap">
        <table aria-label="公交线路">
          <thead><tr><th>编号</th><th>名称</th><th>线色</th><th>站序</th><th>状态</th><th>更新时间</th><th></th></tr></thead>
          <tbody id="br-body"></tbody>
        </table>
        <div class="empty" id="br-empty" hidden><p>还没有线路 —— 点右上角「新建线路」建档，再画走向</p></div>
      </div>

      <h2 class="section-title">站点 <span class="hint">站点先在地图上点选位置，再挂到线路的站序里</span></h2>
      <div class="toolbar">
        <div class="search-box">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
          <input type="text" id="bp-q" placeholder="按站名过滤…" aria-label="过滤站点">
        </div>
        <select id="bp-route" aria-label="按线路过滤"><option value="">全部线路</option>
          ${routes.map(r => `<option value="${r.route_id}">${esc(routeLabel(r))}</option>`).join("")}</select>
        <select id="bp-status" aria-label="按状态过滤">
          <option value="">全部状态</option><option value="published">已发布</option><option value="draft">草稿</option>
        </select>
        <span class="result-count" id="bp-count"></span>
      </div>
      <div class="card table-wrap">
        <table aria-label="公交站点">
          <thead><tr><th>ID</th><th>站名</th><th>停靠线路</th><th>坐标</th><th>状态</th><th></th></tr></thead>
          <tbody id="bp-body"></tbody>
        </table>
        <div class="empty" id="bp-empty" hidden><p>还没有站点 —— 用右上角「地图上新增站点」点选位置</p></div>
      </div>

      <h2 class="section-title">车辆 <span class="hint">先注册车辆，车载设备才能上报位置（POST /bus/positions）</span></h2>
      <div class="toolbar">
        <select id="bv-route" aria-label="按线路过滤"><option value="">全部线路</option>
          ${routes.map(r => `<option value="${r.route_id}">${esc(routeLabel(r))}</option>`).join("")}</select>
        <span class="result-count" id="bv-count"></span>
        <button class="btn btn-sm" id="bus-new-vehicle" type="button">${ICON.add} 注册车辆</button>
      </div>
      <div class="card table-wrap">
        <table aria-label="公交车辆">
          <thead><tr><th>车辆编号</th><th>显示名</th><th>值勤线路</th><th>状态</th><th>最新位置</th><th></th></tr></thead>
          <tbody id="bv-body"></tbody>
        </table>
        <div class="empty" id="bv-empty" hidden><p>还没有车辆 —— 点「注册车辆」录入车载设备的编号</p></div>
      </div>`;

    let rq = "", rstatus = "", pq = "", proute = "", pstatus = "", vroute = "";
    const reload = async () => {
      dataChanged("bus");
      [routes, stops, vehicles] = await Promise.all([loadRoutes(), loadStops(), loadVehicles()]);
      renderRoutes(); renderStops(); renderVehicles();
    };

    /* ---- 线路表 ---- */
    function renderRoutes() {
      const kw = rq.trim().toLowerCase();
      const rows = routes.filter(r =>
        (!kw || `${r.code} ${r.name}`.toLowerCase().includes(kw)) && (!rstatus || r.status === rstatus));
      $("#br-body").innerHTML = rows.map(r => `
        <tr data-id="${r.route_id}">
          <td class="cell-id">${esc(r.code)}</td>
          <td><strong>${esc(r.name)}</strong>${r.description ? `<div class="cell-sub">${esc(r.description)}</div>` : ""}</td>
          <td>${r.color
            ? `<span class="swatch-dot" style="background:${esc(r.color)}"></span><span class="cell-id">${esc(r.color)}</span>`
            : '<span class="cell-sub">默认</span>'}</td>
          <td>${r.stop_count} 站${r.is_loop ? ' <span class="badge blue">环线</span>' : ""}</td>
          <td>${STATUS_BADGE[r.status] || esc(r.status)}</td>
          <td class="cell-id">${ts(r.updated_at)}</td>
          <td><div class="row-actions">
            ${iconBtn(ICON.order, "order", "排站序")}
            ${iconBtn(ICON.path, "draw", "在地图上画走向")}
            ${iconBtn(ICON.edit, "edit", "编辑线路档案")}
            ${iconBtn(ICON.trash, "del", "删除线路")}
          </div></td>
        </tr>`).join("");
      $("#br-empty").hidden = rows.length > 0;
      $("#br-count").textContent = `共 ${rows.length} 条`;
    }

    /* ---- 站点表 ---- */
    function renderStops() {
      const kw = pq.trim().toLowerCase();
      const rows = stops.filter(s =>
        (!kw || s.name.toLowerCase().includes(kw)) &&
        (!proute || (s.route_codes || []).includes(routeCode(proute))) &&
        (!pstatus || s.status === pstatus));
      $("#bp-body").innerHTML = rows.map(s => `
        <tr data-id="${s.stop_id}">
          <td class="cell-id">${s.stop_id}</td>
          <td><strong>${esc(s.name)}</strong>${s.description ? `<div class="cell-sub">${esc(s.description)}</div>` : ""}</td>
          <td>${(s.route_codes || []).length
            ? s.route_codes.map(c => `<span class="badge blue">${esc(c)}</span>`).join(" ")
            : '<span class="cell-sub">未挂线路</span>'}</td>
          <td class="cell-id">${Number(s.lng).toFixed(5)}, ${Number(s.lat).toFixed(5)}</td>
          <td>${STATUS_BADGE[s.status] || esc(s.status)}</td>
          <td><div class="row-actions">
            ${iconBtn(ICON.locate, "locate", "在地图上定位")}
            ${iconBtn(ICON.move, "move", "在地图上重设位置")}
            ${iconBtn(ICON.edit, "edit", "编辑站点")}
            ${iconBtn(ICON.trash, "del", "删除站点")}
          </div></td>
        </tr>`).join("");
      $("#bp-empty").hidden = rows.length > 0;
      $("#bp-count").textContent = `共 ${rows.length} 条`;
    }
    const routeCode = id => (routes.find(r => String(r.route_id) === String(id)) || {}).code || "";

    /* ---- 车辆表 ---- */
    function renderVehicles() {
      const rows = vehicles.filter(v => !vroute || String(v.route_id) === vroute);
      $("#bv-body").innerHTML = rows.map(v => {
        const hasPos = v.lng != null && v.lat != null;
        const fresh = hasPos && (v.age_s ?? Infinity) <= FRESH_SECONDS;
        const pos = hasPos
          ? `<span class="badge ${fresh ? "ok" : "warn"}">${fresh ? "在线" : "离线"}</span>
             <div class="cell-sub">${Math.round(v.age_s)} 秒前${v.speed_kmh != null ? ` · ${Number(v.speed_kmh).toFixed(0)} km/h` : ""}${v.heading_deg != null ? ` · 朝向 ${Number(v.heading_deg).toFixed(0)}°` : ""}</div>`
          : '<span class="cell-sub">从未上报</span>';
        return `
        <tr data-id="${esc(v.vehicle_id)}">
          <td class="cell-id">${esc(v.vehicle_id)}</td>
          <td>${esc(v.label || "—")}</td>
          <td>${v.route_code ? `<span class="badge blue">${esc(v.route_code)}</span>` : '<span class="cell-sub">未排班</span>'}</td>
          <td>${v.enabled ? '<span class="badge ok">在用</span>' : '<span class="badge warn">已停运</span>'}</td>
          <td>${pos}</td>
          <td><div class="row-actions">
            ${iconBtn(ICON.power, "toggle", v.enabled ? "停运（App 不再下发）" : "恢复使用")}
            ${iconBtn(ICON.edit, "edit", "编辑车辆")}
            ${iconBtn(ICON.trash, "del", "删除车辆（含历史轨迹）")}
          </div></td>
        </tr>`;
      }).join("");
      $("#bv-empty").hidden = rows.length > 0;
      $("#bv-count").textContent = `共 ${rows.length} 辆`;
      $("#bus-sub").textContent = `${routes.length} 条线路 · ${stops.length} 个站点 · ${vehicles.length} 辆车`;
    }

    /* ---- 过滤与操作 ---- */
    $("#br-q").addEventListener("input", e => { rq = e.target.value; renderRoutes(); });
    $("#br-status").addEventListener("change", e => { rstatus = e.target.value; renderRoutes(); });
    $("#bp-q").addEventListener("input", e => { pq = e.target.value; renderStops(); });
    $("#bp-route").addEventListener("change", e => { proute = e.target.value; renderStops(); });
    $("#bp-status").addEventListener("change", e => { pstatus = e.target.value; renderStops(); });
    $("#bv-route").addEventListener("change", e => { vroute = e.target.value; renderVehicles(); });
    $("#bus-new-route").addEventListener("click", () => routeDialog(null, reload));
    $("#bus-new-vehicle").addEventListener("click", () => vehicleDialog(null, routes, reload));

    $("#br-body").addEventListener("click", async e => {
      const btn = e.target.closest("button[data-act]");
      if (!btn) return;
      const route = routes.find(r => String(r.route_id) === btn.closest("tr").dataset.id);
      if (!route) return;
      const act = btn.dataset.act;
      if (act === "edit") return routeDialog(route, reload);
      if (act === "draw") { location.hash = `#/map?draw=bus-route&route=${route.route_id}`; return; }
      if (act === "order") {
        try {
          const detail = await loadRouteDetail(route.route_id);
          stopOrderDialog(detail, reload);
        } catch (err) { toast("读取站序失败：" + err.message, "err"); }
        return;
      }
      confirmDialog("删除线路", `确定删除线路 <b>${esc(routeLabel(route))}</b> 吗？站序会一并删除，App 端不再显示这条线。`, async () => {
        await api(`/api/v1/admin/bus/routes/${route.route_id}`, { method: "DELETE" });
        toast("线路已删除");
        await reload();
      });
    });

    $("#bp-body").addEventListener("click", e => {
      const btn = e.target.closest("button[data-act]");
      if (!btn) return;
      const stop = stops.find(s => String(s.stop_id) === btn.closest("tr").dataset.id);
      if (!stop) return;
      const act = btn.dataset.act;
      if (act === "locate") { location.hash = `#/map?focus=bus-stops:${stop.stop_id}`; return; }
      if (act === "move") { location.hash = `#/map?draw=bus-stop&stop=${stop.stop_id}`; return; }
      if (act === "edit") return stopDialog(stop, null, reload);
      confirmDialog("删除站点", `确定删除站点 <b>${esc(stop.name)}</b> 吗？它在线路站序里的位置也会被移除。`, async () => {
        await api(`/api/v1/admin/bus/stops/${stop.stop_id}`, { method: "DELETE" });
        toast("站点已删除");
        await reload();
      });
    });

    $("#bv-body").addEventListener("click", e => {
      const btn = e.target.closest("button[data-act]");
      if (!btn) return;
      const v = vehicles.find(x => x.vehicle_id === btn.closest("tr").dataset.id);
      if (!v) return;
      const act = btn.dataset.act;
      if (act === "edit") return vehicleDialog(v, routes, reload);
      if (act === "toggle") {
        const next = !v.enabled;
        confirmDialog(next ? "恢复使用" : "停运车辆",
          next ? `确定让 <b>${esc(v.vehicle_id)}</b> 恢复上报？` : `停运后 <b>${esc(v.vehicle_id)}</b> 不再下发给 App，车载设备的上报也会被拒绝。`,
          async () => {
            await api(`/api/v1/admin/bus/vehicles/${encodeURIComponent(v.vehicle_id)}`, { method: "PATCH", body: { enabled: next } });
            toast(next ? "车辆已恢复使用" : "车辆已停运");
            await reload();
          }, next ? "恢复" : "停运");
        return;
      }
      confirmDialog("删除车辆", `确定删除车辆 <b>${esc(v.vehicle_id)}</b> 吗？<b>它的历史轨迹会一并删除</b>；只想停运请用「停运」按钮。`, async () => {
        await api(`/api/v1/admin/bus/vehicles/${encodeURIComponent(v.vehicle_id)}`, { method: "DELETE" });
        toast("车辆已删除");
        await reload();
      });
    });

    renderRoutes(); renderStops(); renderVehicles();
  }

  /* ---------------- 弹窗：线路档案 ---------------- */

  function routeDialog(route, onSaved) {
    const props = (route && route.props) || {};
    openDialog(route ? `编辑线路 · ${routeLabel(route)}` : "新建线路", `
      <div class="field"><label for="br-code">线路编号 <span class="hint">（必填，全库唯一，如 1号线）</span></label>
        <input type="text" id="br-code" value="${esc(route?.code || "")}" required><p class="field-error"></p></div>
      <div class="field"><label for="br-name">名称 <span class="hint">（必填）</span></label>
        <input type="text" id="br-name" value="${esc(route?.name || "")}" placeholder="如: 校园环线" required><p class="field-error"></p></div>
      <div class="field"><label for="br-desc">说明 <span class="hint">（App 详情页可见）</span></label>
        <textarea id="br-desc" rows="2" placeholder="起讫点、途经点等">${esc(route?.description || "")}</textarea></div>
      <div class="field"><label for="br-color">线色 <span class="hint">（App 与底图都用它上色，留空用默认色）</span></label>
        <div class="row-inline">
          <input type="color" id="br-color-pick" value="${esc(route?.color || DEFAULT_COLOR)}" aria-label="选择线色">
          <input type="text" id="br-color" value="${esc(route?.color || "")}" placeholder="#0b7285 或留空" style="font-family:var(--mono)">
          <button class="btn btn-sm" type="button" id="br-color-clear">清除</button>
        </div>
        <p class="field-error"></p></div>
      <div class="field"><label class="check-row"><input type="checkbox" id="br-loop" ${route?.is_loop ? "checked" : ""}> 环线（允许首末站是同一个站）</label></div>
      <div class="field"><label for="br-hours">运营时间 <span class="hint">（可选，存进 props.service_hours）</span></label>
        <input type="text" id="br-hours" value="${esc(props.service_hours || "")}" placeholder="如 07:30-21:30"></div>
      <div class="field"><label for="br-headway">发车间隔（分钟）<span class="hint">（可选，存进 props.headway_min）</span></label>
        <input type="number" id="br-headway" min="0" step="1" value="${props.headway_min ?? ""}"></div>
      <div class="field"><label for="br-status">状态</label>
        <select id="br-status">${statusOptions(route?.status)}</select></div>
      <p class="cell-sub">走向与站序不在这里改：保存后用表格里的「画走向」「排站序」。其它 props 键（脚本写入的）原样保留。</p>`,
      async () => {
        const code = $("#br-code").value.trim();
        const name = $("#br-name").value.trim();
        if (!code) throw new Error("线路编号不能为空");
        if (!name) throw new Error("名称不能为空");
        const color = $("#br-color").value.trim();
        if (color && !/^#[0-9a-fA-F]{6}$/.test(color)) throw new Error("线色应形如 #1a7f37（六位十六进制）");
        const nextProps = { ...props };
        const hours = $("#br-hours").value.trim();
        const headway = $("#br-headway").value.trim();
        if (hours) nextProps.service_hours = hours; else delete nextProps.service_hours;
        if (headway !== "" && Number(headway) >= 0) nextProps.headway_min = Number(headway); else delete nextProps.headway_min;
        const body = {
          code, name, description: $("#br-desc").value.trim(), color,
          is_loop: $("#br-loop").checked, status: $("#br-status").value, props: nextProps,
        };
        if (route) {
          await api(`/api/v1/admin/bus/routes/${route.route_id}`, { method: "PATCH", body });
          toast("线路已保存");
        } else {
          const d = await api("/api/v1/admin/bus/routes", { method: "POST", body });
          toast(`线路已创建（#${d.route_id}），接着画走向排站序`);
        }
        dataChanged("bus");
        await onSaved?.();
      });

    const pick = $("#br-color-pick"), text = $("#br-color");
    pick.addEventListener("input", () => { text.value = pick.value; });
    text.addEventListener("input", () => {
      const v = text.value.trim();
      if (/^#[0-9a-fA-F]{6}$/.test(v)) pick.value = v;
    });
    $("#br-color-clear").addEventListener("click", () => { text.value = ""; });
  }

  /* ---------------- 弹窗：排站序 ---------------- */
  /* 站序是"顺序"而不是"集合"，所以用「列表 + 上移/下移/移除 + 从下拉追加」，
   * 与后端 PUT /admin/bus/routes/:id/stops 的 stop_ids 数组一一对应。 */
  function stopOrderDialog(route, onSaved) {
    let order = (route.stops || []).map(s => ({ stop_id: s.stop_id, name: s.name }));
    const menuStops = cache.stops || [];
    const options = menuStops.length
      ? menuStops.map(s => `<option value="${s.stop_id}">${esc(s.name)}</option>`).join("")
      : '<option value="">（还没有站点，先用「地图上新增站点」）</option>';

    openDialog(`排站序 · ${routeLabel(route)}`, `
      <div class="field"><label>当前站序 <span class="hint">（↑ ↓ 调整顺序，✕ 移除）</span></label>
        <div class="order-list" id="bo-list"></div></div>
      <div class="field"><label for="bo-add">追加站点</label>
        <div class="row-inline">
          <select id="bo-add">${options}</select>
          <button class="btn btn-sm" type="button" id="bo-add-btn">加到末尾</button>
        </div></div>
      <p class="cell-sub">规则：至少 2 站；同一站在中间不能重复；${route.is_loop
        ? "本线路是环线，首末站可以同站（起点即终点）。"
        : "本线路不是环线，首末站不能同站 —— 要成环请先在「编辑线路」里勾上环线。"}<br>
        清空列表后保存 = 取消这条线的全部站点（线路本身保留）。</p>`,
      async () => {
        const ids = order.map(o => o.stop_id);
        if (ids.length === 1) throw new Error("线路至少要有 2 个站点（起点与终点）");
        const seen = new Set();
        ids.forEach((id, i) => {
          if (seen.has(id) && !(route.is_loop && i === ids.length - 1 && ids[0] === id)) {
            throw new Error(`站点「${order[i].name}」重复出现；只有环线的首末站可以同站`);
          }
          seen.add(id);
        });
        await api(`/api/v1/admin/bus/routes/${route.route_id}/stops`, {
          method: "PUT", body: { stop_ids: ids },
        });
        toast("站序已保存");
        dataChanged("bus");
        await onSaved?.();
      });

    const listEl = $("#bo-list");
    function renderOrder() {
      if (!order.length) { listEl.innerHTML = '<p class="order-empty">还没有站点</p>'; return; }
      listEl.innerHTML = order.map((o, i) => `
        <div class="order-row" data-i="${i}">
          <span class="seq">${i + 1}</span>
          <span class="name">${esc(o.name)}</span>
          <button class="icon-btn" data-mv="up" type="button" title="上移" aria-label="上移" ${i === 0 ? "disabled" : ""}>↑</button>
          <button class="icon-btn" data-mv="down" type="button" title="下移" aria-label="下移" ${i === order.length - 1 ? "disabled" : ""}>↓</button>
          <button class="icon-btn" data-mv="del" type="button" title="移除" aria-label="移除">✕</button>
        </div>`).join("");
    }
    listEl.addEventListener("click", e => {
      const btn = e.target.closest("button[data-mv]");
      if (!btn) return;
      const i = Number(btn.closest(".order-row").dataset.i);
      const mv = btn.dataset.mv;
      if (mv === "up" && i > 0) [order[i - 1], order[i]] = [order[i], order[i - 1]];
      else if (mv === "down" && i < order.length - 1) [order[i + 1], order[i]] = [order[i], order[i + 1]];
      else if (mv === "del") order.splice(i, 1);
      renderOrder();
    });
    $("#bo-add-btn").addEventListener("click", () => {
      const stop = menuStops.find(s => String(s.stop_id) === $("#bo-add").value);
      if (!stop) { toast("还没有可用站点", "err"); return; }
      order.push({ stop_id: stop.stop_id, name: stop.name });
      renderOrder();
    });
    renderOrder();
  }

  /* ---------------- 弹窗：站点 ---------------- */

  function stopDialog(stop, picked, onSaved) {
    const lng = stop ? stop.lng : picked.lng;
    const lat = stop ? stop.lat : picked.lat;
    openDialog(stop ? `编辑站点 · ${stop.name}` : "新建站点", `
      <div class="field"><label>位置</label>
        <input type="text" value="${Number(lng).toFixed(6)}, ${Number(lat).toFixed(6)}" readonly
               style="background:var(--muted);font-family:var(--mono)">
        <p class="cell-sub">${stop ? "要挪位置请用站点表里的「重设位置」在地图上重新点选。" : "位置就是刚才在地图上点的地方。"}</p></div>
      <div class="field"><label for="bs-name">站名 <span class="hint">（必填，App 与底图都显示它）</span></label>
        <input type="text" id="bs-name" value="${esc(stop?.name || "")}" required><p class="field-error"></p></div>
      <div class="field"><label for="bs-desc">说明</label>
        <textarea id="bs-desc" rows="2" placeholder="站台位置、周边标志物等">${esc(stop?.description || "")}</textarea></div>
      <div class="field"><label for="bs-status">状态</label>
        <select id="bs-status">${statusOptions(stop?.status)}</select></div>`,
      async () => {
        const name = $("#bs-name").value.trim();
        if (!name) throw new Error("站名不能为空");
        const body = {
          name, description: $("#bs-desc").value.trim(), status: $("#bs-status").value,
          geometry: { type: "Point", coordinates: [Number(lng), Number(lat)] },
        };
        if (stop) {
          delete body.geometry; // 位置改不了，免得把没动的坐标又写一遍
          await api(`/api/v1/admin/bus/stops/${stop.stop_id}`, { method: "PATCH", body });
          toast("站点已保存");
        } else {
          const d = await api("/api/v1/admin/bus/stops", { method: "POST", body });
          toast(`站点已创建（#${d.stop_id}）`);
        }
        dataChanged("bus");
        await onSaved?.();
      });
  }

  /* ---------------- 弹窗：车辆 ---------------- */

  function vehicleDialog(vehicle, routes, onSaved) {
    const routeOptions = ['<option value="">— 未排班 —</option>']
      .concat(routes.map(r => `<option value="${r.route_id}"${vehicle && vehicle.route_id === r.route_id ? " selected" : ""}>${esc(routeLabel(r))}</option>`))
      .join("");
    openDialog(vehicle ? `编辑车辆 · ${vehicle.vehicle_id}` : "注册车辆", `
      <div class="field"><label for="bv-id">车辆编号 <span class="hint">（必填，车载设备/司机端自带，注册后不改）</span></label>
        <input type="text" id="bv-id" value="${esc(vehicle?.vehicle_id || "")}" ${vehicle ? 'readonly style="background:var(--muted);font-family:var(--mono)"' : 'placeholder="如 BUS-01"'} required>
        <p class="field-error"></p></div>
      <div class="field"><label for="bv-label">显示名 <span class="hint">（可选，如 1号车）</span></label>
        <input type="text" id="bv-label" value="${esc(vehicle?.label || "")}"></div>
      <div class="field"><label for="bv-route">值勤线路</label>
        <select id="bv-route">${routeOptions}</select>
        <p class="cell-sub">选「未排班」即把车辆从线路上摘下来（位置照常上报，只是不挂在线上）。</p></div>
      <div class="field"><label class="check-row"><input type="checkbox" id="bv-on" ${!vehicle || vehicle.enabled ? "checked" : ""}> 在用（停运后 App 不下发，且拒绝该车的位置上报）</label></div>`,
      async () => {
        const id = $("#bv-id").value.trim();
        if (!id) throw new Error("车辆编号不能为空");
        const routeId = Number($("#bv-route").value) || 0; // 0 = 未排班（后端按清空处理）
        if (vehicle) {
          await api(`/api/v1/admin/bus/vehicles/${encodeURIComponent(vehicle.vehicle_id)}`, {
            method: "PATCH",
            body: { label: $("#bv-label").value.trim(), route_id: routeId, enabled: $("#bv-on").checked },
          });
          toast("车辆已保存");
        } else {
          await api("/api/v1/admin/bus/vehicles", {
            method: "POST",
            body: { vehicle_id: id, label: $("#bv-label").value.trim(), route_id: routeId || null, enabled: $("#bv-on").checked },
          });
          toast(`车辆已注册（${id}）`);
        }
        dataChanged("bus");
        await onSaved?.();
      });
  }

  /* ---------------- 地图图层：线路 / 站点 / 实时车辆 ---------------- */
  /* 三个插件共用 dataKey="bus"：dataChanged("bus") 一次把三层的取数缓存都刷掉。
   * 线路数据来自 /admin/bus/geometry（含草稿），车辆位置来自 /bus/vehicles（只出新鲜位置）。 */
  const busGeo = () => geoOnce("bus", "/api/v1/admin/bus/geometry");

  K.registerMapPlugin({
    id: "bus-routes", dataKey: "bus", label: "公交线路", color: DEFAULT_COLOR, on: true,
    load: async () => (await busGeo()).features.filter(f => f.properties.layer === "bus_route"),
    layers: [{ id: "l-bus-route", type: "line",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": ["case", ["==", ["get", "color"], ""], DEFAULT_COLOR, ["get", "color"]],
        "line-width": ["interpolate", ["linear"], ["zoom"], 12, 1.6, 17, 4.5],
      } }],
    focusId: f => String(f.properties.route_id),
    popup: f => {
      const p = f.properties;
      return `
        <h4>${esc(p.code)} ${esc(p.name)}</h4>
        <div class="kv">${p.is_loop ? "环线" : "往返线"}${p.color ? " · " + esc(p.color) : " · 默认线色"}</div>
        <div class="popup-actions">
          <a class="btn btn-sm" href="#/bus">线路管理</a>
          <button class="btn btn-sm btn-danger" data-act="remove" type="button">删除</button>
        </div>`;
    },
    remove: f => confirmDialog("删除线路",
      `确定删除线路 <b>${esc(f.properties.code)}</b> 吗？站序会一并删除。`, async () => {
        await api(`/api/v1/admin/bus/routes/${f.properties.route_id}`, { method: "DELETE" });
        toast("线路已删除");
        dataChanged("bus");
      }),
  });

  K.registerMapPlugin({
    id: "bus-stops", dataKey: "bus", label: "公交站点", color: "#0369A1", on: true,
    load: async () => (await busGeo()).features.filter(f => f.properties.layer === "bus_stop"),
    layers: [{ id: "l-bus-stop", type: "circle",
      paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 3, 18, 6.5],
               "circle-color": "#ffffff", "circle-stroke-width": 2, "circle-stroke-color": "#0369A1" } }],
    focusId: f => String(f.properties.stop_id),
    popup: f => {
      const p = f.properties;
      const codes = p.route_codes || [];
      return `
        <h4>${esc(p.name)}</h4>
        <div class="kv">${codes.length ? "停靠：" + codes.map(c => esc(c)).join("、") : "未挂线路"}</div>
        <div class="popup-actions">
          <a class="btn btn-sm" href="#/bus">站点管理</a>
          <button class="btn btn-sm btn-danger" data-act="remove" type="button">删除</button>
        </div>`;
    },
    remove: f => confirmDialog("删除站点",
      `确定删除站点 <b>${esc(f.properties.name)}</b> 吗？它在线路站序里的位置也会被移除。`, async () => {
        await api(`/api/v1/admin/bus/stops/${f.properties.stop_id}`, { method: "DELETE" });
        toast("站点已删除");
        dataChanged("bus");
      }),
  });

  K.registerMapPlugin({
    id: "bus-vehicles", dataKey: "bus", label: "实时车辆", color: "#DC2626", on: true,
    refreshMs: 15000, // 轮询：地图挂载期间每 15 秒就地刷新（页面隐藏时跳过）
    load: async () => ((await api("/api/v1/bus/vehicles")).vehicles || [])
      .filter(v => v.lng != null && v.lat != null)
      .map(v => ({ type: "Feature", geometry: { type: "Point", coordinates: [v.lng, v.lat] }, properties: v })),
    layers: [
      { id: "l-bus-vehicle-halo", type: "circle",
        paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 7, 17, 15],
                 "circle-color": "#DC2626", "circle-opacity": .16 } },
      { id: "l-bus-vehicle", type: "circle",
        paint: { "circle-radius": ["interpolate", ["linear"], ["zoom"], 12, 3.5, 17, 7],
                 "circle-color": "#DC2626", "circle-stroke-width": 1.5, "circle-stroke-color": "#ffffff" } },
    ],
    popup: f => {
      const p = f.properties;
      const age = Math.round(p.age_s ?? 0);
      return `
        <h4>${esc(p.label || p.vehicle_id)}</h4>
        <div class="kv">${esc(p.route_code || "未排班")} · ${age <= FRESH_SECONDS ? "在线" : "离线"}（${age} 秒前）</div>
        <div class="kv">${p.speed_kmh != null ? Number(p.speed_kmh).toFixed(0) + " km/h" : "速度未知"}${p.heading_deg != null ? " · 朝向 " + Number(p.heading_deg).toFixed(0) + "°" : ""}</div>
        <div class="popup-actions"><a class="btn btn-sm" href="#/bus">车辆管理</a></div>`;
    },
  });

  /* ---------------- 绘制任务 ---------------- */
  /* #/map?draw=bus-route&route=<id>  画/重画线路走向（线）
   * #/map?draw=bus-stop             在地图上点选位置新建站点
   * #/map?draw=bus-stop&stop=<id>   重设某个站点的位置 */
  K.registerDrawTask("bus-route", async params => {
    const routeId = Number(params.get("route"));
    if (!routeId) { toast("缺少线路编号：请从线路表点「画走向」", "err"); return null; }
    const route = await api(`/api/v1/admin/bus/routes/${routeId}`);
    return {
      type: "line", minPoints: 2,
      hint: `画「${routeLabel(route)}」的走向：依次点击拐点；按 Enter 或点回最后一个点完成，Esc 取消`,
      onFinish: async geometry => {
        await api(`/api/v1/admin/bus/routes/${routeId}`, { method: "PATCH", body: { geometry } });
        toast(`「${routeLabel(route)}」走向已更新`);
        dataChanged("bus");
      },
    };
  });

  K.registerDrawTask("bus-stop", async params => {
    const stopId = Number(params.get("stop"));
    if (!stopId) {
      return {
        type: "point", hint: "点击站点的位置；Esc 取消",
        onFinish: geometry => stopDialog(null, { lng: geometry.coordinates[0], lat: geometry.coordinates[1] }, null),
      };
    }
    const stops = (await api("/api/v1/admin/bus/stops")).stops || [];
    const stop = stops.find(s => s.stop_id === stopId);
    if (!stop) { toast("站点不存在", "err"); return null; }
    return {
      type: "point", hint: `重设「${stop.name}」的位置：点击新位置，Esc 取消`,
      onFinish: async geometry => {
        await api(`/api/v1/admin/bus/stops/${stopId}`, { method: "PATCH", body: { geometry } });
        toast("站点位置已更新");
        dataChanged("bus");
      },
    };
  });

  /* ---------------- 注册 ---------------- */
  K.registerView("bus", view);
  // app.js 启动时还不认识这个视图（脚本顺序如此），若当前就在公交页要补一次渲染
  if (location.hash.replace(/^#\/?/, "").split("?")[0] === "bus") K.reroute();

  return { view, openStopOrder: stopOrderDialog };
})();
