/* Three.js module failures must stay visible even before the viewer initializes. */
"use strict";
import("./model-viewer.js").catch(error => {
  const message = `查看器组件加载失败：${error.message || error}`;
  document.getElementById("load-title").textContent = "无法启动查看器";
  document.getElementById("load-message").textContent = message;
  document.getElementById("load-spinner").hidden = true;
  document.getElementById("load-progress").hidden = true;
  document.getElementById("viewport").setAttribute("aria-busy", "false");
  const retry = document.getElementById("retry");
  retry.hidden = false;
  retry.onclick = () => location.reload();
  try { window.CampusModelViewer?.postMessage(JSON.stringify({ type: "error", message })); } catch { /* standalone browser */ }
});
