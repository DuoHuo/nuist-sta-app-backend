/* Building model management. Multipart uploads deliberately omit Content-Type. */
"use strict";
window.CampusModels = (() => {
  const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const bytes = value => Number.isFinite(Number(value)) ? `${(Number(value) / 1048576).toFixed(2)} MB` : "—";
  const viewerURL = id => `/admin/model-viewer.html?building_id=${encodeURIComponent(id)}&floor=all`;
  function modelURL(value) {
    const url = new URL(value, location.href);
    if (url.origin !== location.origin || !/^https?:$/.test(url.protocol)) throw new Error("模型地址必须来自本站");
    return url.href;
  }
  async function metadata(id) {
    const response = await fetch(`/api/v1/buildings/${encodeURIComponent(id)}/model`, { cache: "no-store", headers: { Accept: "application/json" } });
    const payload = await response.json();
    if (!response.ok || (payload.code && payload.code !== "ok")) {
      const error = new Error(payload.message || `HTTP ${response.status}`);
      error.status = response.status;
      throw error;
    }
    return payload.data || payload;
  }
  function attach(container, building) {
    container.innerHTML = `<h3>三维模型</h3><p class="model-summary" role="status">正在读取模型…</p>
      <div class="model-actions"><button type="button" class="btn" data-upload>上传模型</button>
      <a class="btn" target="_blank" rel="noopener" href="${viewerURL(building.building_id)}">打开查看器</a>
      <a class="btn" data-download hidden download>下载整栋 GLB</a></div>`;
    const summary = container.querySelector(".model-summary");
    async function refresh() {
      const download = container.querySelector("[data-download]");
      download.hidden = true;
      try {
        const model = await metadata(building.building_id);
        summary.textContent = `版本 ${model.version ?? "—"} · ${bytes(model.size_bytes)} · ${(model.floors || []).length} 层`;
        summary.title = model.sha256 ? `SHA-256: ${model.sha256}` : "";
        if (model.url) { download.href = modelURL(model.url); download.hidden = false; }
      } catch (error) {
        summary.textContent = error.status === 404 ? "尚未上传模型。上传后可在手机和网页中查看。" : `模型信息读取失败：${error.message}`;
      }
    }
    container.querySelector("[data-upload]").addEventListener("click", () => uploadDialog(building, refresh));
    refresh();
  }
  async function validateGLB(file) {
    if (!file || !/\.glb$/i.test(file.name)) throw new Error("请选择 .glb 模型文件");
    const header = await file.slice(0, 12).arrayBuffer();
    if (header.byteLength < 12) throw new Error(`${file.name} 不是有效的 GLB`);
    const view = new DataView(header);
    if (view.getUint32(0, true) !== 0x46546c67 || view.getUint32(4, true) !== 2 || view.getUint32(8, true) !== file.size) {
      throw new Error(`${file.name} 不是完整的 GLB 2.0 文件`);
    }
  }
  function uploadDialog(building, onSaved) {
    const dialog = document.createElement("dialog");
    dialog.className = "dlg model-upload-dialog";
    dialog.setAttribute("aria-labelledby", "model-upload-title");
    dialog.innerHTML = `<form novalidate>
      <header class="dlg-head"><h2 id="model-upload-title">上传模型 · ${escapeHTML(building.name)}</h2><button class="btn" type="button" data-close>关闭</button></header>
      <div class="dlg-body">
        <p class="model-help">上传后替换该建筑的当前模型。整栋 GLB 和 manifest.json 必填，分层模型可选。</p>
        <div class="field"><label for="model-file">整栋模型（.glb）</label><input id="model-file" type="file" accept=".glb,model/gltf-binary" required></div>
        <div class="field"><label for="model-manifest">楼层清单（manifest.json）</label><input id="model-manifest" type="file" accept=".json,application/json" required>
        <p class="model-help">清单中的 floors 定义楼层编号、名称、节点名和标高。选择后可逐层附加 GLB；未附加的楼层按整栋模型节点显示。</p></div>
        <div data-floors></div>
        <p class="model-help" data-progress role="status" aria-live="polite"></p><progress max="100" value="0" hidden aria-label="模型上传进度"></progress>
        <p class="dlg-error" data-error role="alert" tabindex="-1"></p>
      </div>
      <footer class="dlg-foot"><button class="btn btn-primary" type="submit">上传并保存</button></footer>
    </form>`;
    document.body.appendChild(dialog);
    const form = dialog.querySelector("form");
    const errorBox = dialog.querySelector("[data-error]");
    const manifestInput = dialog.querySelector("#model-manifest");
    const floorsBox = dialog.querySelector("[data-floors]");
    const progressText = dialog.querySelector("[data-progress]");
    const progress = dialog.querySelector("progress");
    let manifest = null, busy = false, manifestSequence = 0;
    const setError = message => { errorBox.textContent = message; if (message) errorBox.focus(); };
    manifestInput.addEventListener("change", async () => {
      const sequence = ++manifestSequence;
      manifest = null;
      floorsBox.replaceChildren();
      setError("");
      try {
        const file = manifestInput.files[0];
        if (!file) return;
        const parsed = JSON.parse(await file.text());
        if (sequence !== manifestSequence) return;
        if (!parsed || !Array.isArray(parsed.floors) || !parsed.floors.length) throw new Error("manifest.json 必须包含非空 floors 数组");
        const indexes = new Set();
        for (const floor of parsed.floors) {
          if (!floor || !Number.isInteger(floor.level_index) || indexes.has(floor.level_index)) throw new Error("每个楼层需要唯一的整数 level_index");
          indexes.add(floor.level_index);
        }
        manifest = parsed;
        floorsBox.innerHTML = `<h3>分层 GLB <span class="hint">（可选）</span></h3>${parsed.floors.map(floor => `<div class="field"><label for="model-floor-${floor.level_index}">${escapeHTML(floor.display_name || `${floor.level_index + 1}F`)} <span class="hint">· level_index ${floor.level_index}</span></label><input id="model-floor-${floor.level_index}" data-level="${floor.level_index}" type="file" accept=".glb,model/gltf-binary"></div>`).join("")}`;
      } catch (error) { if (sequence === manifestSequence) setError(`清单无效：${error.message}`); }
    });
    dialog.querySelector("[data-close]").addEventListener("click", () => dialog.close());
    dialog.addEventListener("cancel", event => { if (busy) event.preventDefault(); });
    dialog.addEventListener("close", () => dialog.remove(), { once: true });
    form.addEventListener("submit", async event => {
      event.preventDefault();
      if (busy) return;
      busy = true;
      setError("");
      const controls = [...form.querySelectorAll("input, button")];
      controls.forEach(control => { control.disabled = true; });
      try {
        const file = dialog.querySelector("#model-file").files[0];
        await validateGLB(file);
        if (!manifest || !manifestInput.files[0]) throw new Error("请先选择有效的 manifest.json");
        const data = new FormData();
        data.append("file", file);
        data.append("manifest", manifestInput.files[0], "manifest.json");
        for (const input of floorsBox.querySelectorAll("input[data-level]")) {
          if (input.files[0]) { await validateGLB(input.files[0]); data.append(`floor_${input.dataset.level}`, input.files[0]); }
          else {
            const floor = manifest.floors.find(item => String(item.level_index) === input.dataset.level);
            if (!floor.node_name) throw new Error(`${floor.display_name || floor.level_index} 未选择分层 GLB，清单需提供 node_name`);
          }
        }
        progress.hidden = false;
        progress.value = 0;
        progressText.textContent = "正在上传模型…";
        await new Promise((resolve, reject) => {
          const xhr = new XMLHttpRequest();
          xhr.open("POST", `/api/v1/admin/buildings/${encodeURIComponent(building.building_id)}/model`);
          xhr.timeout = 600000;
          xhr.setRequestHeader("Accept", "application/json");
          const token = localStorage.getItem("admin_token");
          if (token) xhr.setRequestHeader("X-Collect-Token", token);
          xhr.upload.onprogress = e => {
            if (e.lengthComputable) {
              progress.value = e.loaded / e.total * 100;
              progressText.textContent = e.loaded === e.total ? "上传完成，服务器正在保存…" : `正在上传 ${Math.round(progress.value)}%`;
            }
          };
          xhr.onload = () => {
            let payload;
            try { payload = JSON.parse(xhr.responseText); } catch { /* non-JSON gateway errors */ }
            if (xhr.status >= 200 && xhr.status < 300 && (!payload?.code || payload.code === "ok")) resolve(payload);
            else reject(new Error(xhr.status === 401 ? "鉴权失败：请在管理台设置正确的管理令牌" : payload?.message || `上传失败（HTTP ${xhr.status}）`));
          };
          xhr.onerror = () => reject(new Error("网络连接失败，请检查连接后重试"));
          xhr.ontimeout = () => reject(new Error("上传超时，请检查模型状态后重试"));
          xhr.send(data);
        });
        await onSaved();
        dialog.close();
      } catch (error) {
        progressText.textContent = "未完成保存，可保留当前文件重试。";
        setError(error.message);
      } finally {
        busy = false;
        controls.forEach(control => { control.disabled = false; });
      }
    });
    dialog.showModal();
    dialog.querySelector("#model-file").focus();
  }
  return { attach };
})();
