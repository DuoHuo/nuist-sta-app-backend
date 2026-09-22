import * as THREE from "./vendor/three/three.module.js";
import { GLTFLoader } from "./vendor/three/GLTFLoader.js";
import { OrbitControls } from "./vendor/three/OrbitControls.js";

const $ = id => document.getElementById(id);
const viewport = $("viewport");
const params = new URLSearchParams(location.search);
const buildingId = params.get("building_id")?.trim();
let selectedFloor = params.get("floor") || "all";
let metadata, renderer, scene, camera, controls, modelRoot, grid, selection;
let controller, generation = 0, frame = 0, rooms = [], selectedMaterials = [];
let contextLost = false;
const pointer = new THREE.Vector2();
const raycaster = new THREE.Raycaster();
const loader = new GLTFLoader();

function notify(type, message) {
  try { window.CampusModelViewer?.postMessage(JSON.stringify({ type, ...(message ? { message } : {}) })); } catch { /* browser without Flutter channel */ }
}
function status(message, failed = false) {
  $("load-panel").hidden = false;
  $("load-title").textContent = failed ? "模型加载失败" : "正在加载模型";
  $("load-message").textContent = message;
  $("load-spinner").hidden = failed;
  $("load-progress").hidden = failed;
  $("load-progress").removeAttribute("value");
  $("retry").hidden = !failed;
  viewport.setAttribute("aria-busy", String(!failed));
  if (failed) notify("error", message);
}
function sameOriginURL(value) {
  if (!value) throw new Error("模型缺少下载地址");
  const url = new URL(value, location.href);
  if (url.origin !== location.origin || !/^https?:$/.test(url.protocol)) throw new Error("模型地址必须来自本站");
  return url;
}
function render() {
  frame = 0;
  if (renderer && !contextLost) renderer.render(scene, camera);
}
function requestRender() { if (!frame) frame = requestAnimationFrame(render); }
function setupScene() {
  if (renderer) return;
  scene = new THREE.Scene();
  scene.background = new THREE.Color("#EDF2F9");
  camera = new THREE.PerspectiveCamera(42, 1, 0.01, 10000);
  renderer = new THREE.WebGLRenderer({ antialias: true, alpha: false });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.25;
  const canvas = renderer.domElement;
  canvas.setAttribute("role", "img");
  canvas.setAttribute("aria-label", "建筑三维模型。拖动旋转，滚轮或双指缩放；也可使用视角按钮及房间列表。");
  viewport.prepend(canvas);
  scene.add(new THREE.HemisphereLight(0xffffff, 0x8d9bb0, 2.3));
  const sunlight = new THREE.DirectionalLight(0xffffff, 2.8);
  sunlight.position.set(30, 70, 40);
  scene.add(sunlight);
  controls = new OrbitControls(camera, canvas);
  controls.enableDamping = false; // render on demand to keep embedded WebViews cool
  controls.maxPolarAngle = Math.PI * 0.49;
  controls.addEventListener("change", requestRender);
  new ResizeObserver(resize).observe(viewport);
  let down = null, dragged = false;
  const pointers = new Set();
  canvas.addEventListener("pointerdown", event => {
    pointers.add(event.pointerId);
    if (pointers.size === 1) { down = { x: event.clientX, y: event.clientY }; dragged = false; }
    else dragged = true;
  });
  canvas.addEventListener("pointermove", event => {
    if (down && Math.hypot(event.clientX - down.x, event.clientY - down.y) > 6) dragged = true;
  });
  canvas.addEventListener("pointerup", event => {
    pointers.delete(event.pointerId);
    if (down && !dragged && pointers.size === 0 && event.button === 0) pickRoom(event);
    if (!pointers.size) down = null;
  });
  canvas.addEventListener("pointercancel", event => { pointers.delete(event.pointerId); down = null; dragged = true; });
  canvas.addEventListener("webglcontextlost", event => {
    event.preventDefault();
    contextLost = true;
    generation++;
    controller?.abort();
    status("图形上下文已中断，请重新加载查看器。", true);
  });
  canvas.addEventListener("webglcontextrestored", () => { contextLost = false; start(); });
  resize();
}
function resize() {
  if (!renderer) return;
  const width = Math.max(1, viewport.clientWidth), height = Math.max(1, viewport.clientHeight);
  renderer.setSize(width, height, false);
  camera.aspect = width / height;
  camera.updateProjectionMatrix();
  if (modelRoot) fitCamera();
  requestRender();
}
function dispose(root) {
  const geometries = new Set(), materials = new Set(), textures = new Set();
  root?.traverse(object => {
    if (object.geometry) geometries.add(object.geometry);
    for (const material of Array.isArray(object.material) ? object.material : object.material ? [object.material] : []) {
      materials.add(material);
      Object.values(material).forEach(value => { if (value?.isTexture) textures.add(value); });
    }
  });
  textures.forEach(texture => { texture.source?.data?.close?.(); texture.dispose(); });
  materials.forEach(material => material.dispose());
  geometries.forEach(geometry => geometry.dispose());
}
function clearModel() {
  clearSelection();
  if (modelRoot) { scene.remove(modelRoot); dispose(modelRoot); modelRoot = null; }
  if (grid) { scene.remove(grid); dispose(grid); grid = null; }
  rooms = [];
  $("room-select").replaceChildren(new Option("请先加载模型", ""));
  requestRender();
}
function visible(object) {
  for (let node = object; node; node = node.parent) if (!node.visible) return false;
  return true;
}
function visibleBounds(root) {
  root.updateMatrixWorld(true);
  const box = new THREE.Box3();
  root.traverse(object => {
    if (object.isMesh && visible(object)) {
      if (!object.geometry.boundingBox) object.geometry.computeBoundingBox();
      box.union(object.geometry.boundingBox.clone().applyMatrix4(object.matrixWorld));
    }
  });
  return box;
}
function fitCamera() {
  if (!modelRoot) return;
  const box = visibleBounds(modelRoot);
  if (box.isEmpty()) return;
  const center = box.getCenter(new THREE.Vector3());
  const radius = Math.max(box.getSize(new THREE.Vector3()).length() / 2, 0.5);
  const verticalFov = THREE.MathUtils.degToRad(camera.fov);
  const horizontalFov = 2 * Math.atan(Math.tan(verticalFov / 2) * camera.aspect);
  const distance = radius / Math.sin(Math.min(verticalFov, horizontalFov) / 2) * 1.16;
  camera.near = Math.max(0.01, radius / 1000);
  camera.far = Math.max(1000, distance * 15);
  camera.position.copy(center).add(new THREE.Vector3(1, 0.85, 1).normalize().multiplyScalar(distance));
  camera.updateProjectionMatrix();
  controls.target.copy(center);
  controls.minDistance = Math.max(radius * 0.06, 0.2);
  controls.maxDistance = distance * 4;
  controls.update();
  requestRender();
}
function filterFloor(root, floor) {
  const matches = [];
  root.traverse(node => {
    const raw = node.userData?.level_index;
    if ((floor.node_name && (node.name === floor.node_name || node.userData?.name === floor.node_name)) ||
      (raw !== undefined && raw !== null && raw !== "" && Number(raw) === floor.level_index)) matches.push(node);
  });
  if (!matches.length) throw new Error(`整栋模型中找不到 ${floor.display_name || floor.node_name || floor.level_index} 的楼层节点，请上传分层 GLB 或检查清单`);
  const keep = new Set();
  matches.forEach(match => {
    match.traverse(node => keep.add(node));
    for (let node = match; node; node = node.parent) keep.add(node);
  });
  root.traverse(node => { node.visible = node.visible && keep.has(node); });
}
async function fetchGLB(url, signal, sequence) {
  const response = await fetch(url, { signal });
  if (!response.ok) throw new Error(`模型文件无法下载（HTTP ${response.status}）`);
  const total = Number(response.headers.get("content-length"));
  if (!response.body) return response.arrayBuffer();
  const reader = response.body.getReader(), chunks = [];
  let loaded = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
    loaded += value.byteLength;
    if (sequence === generation) {
      $("load-message").textContent = `正在下载模型 · ${(loaded / 1048576).toFixed(1)} MB`;
      if (total > 0) $("load-progress").value = Math.min(99, loaded / total * 100);
    }
  }
  const bytes = new Uint8Array(loaded);
  let offset = 0;
  chunks.forEach(chunk => { bytes.set(chunk, offset); offset += chunk.byteLength; });
  return bytes.buffer;
}
function floorButtons() {
  const nav = $("floor-nav");
  nav.replaceChildren();
  [{ level_index: "all", display_name: "整栋" }, ...metadata.floors].forEach(floor => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "btn";
    button.textContent = floor.display_name || `${floor.level_index + 1}F`;
    button.dataset.floor = floor.level_index;
    button.setAttribute("aria-pressed", String(String(floor.level_index) === selectedFloor));
    button.addEventListener("click", () => {
      selectedFloor = String(floor.level_index);
      const url = new URL(location.href);
      url.searchParams.set("floor", selectedFloor);
      history.replaceState(null, "", url);
      loadFloor();
    });
    nav.appendChild(button);
  });
}
async function loadFloor() {
  const sequence = ++generation;
  controller?.abort();
  controller = new AbortController();
  const { signal } = controller;
  const timeout = setTimeout(() => controller?.signal === signal && controller.abort(), 120000);
  clearModel();
  status("正在下载模型…");
  [...$("floor-nav").children].forEach(button => button.setAttribute("aria-pressed", String(button.dataset.floor === selectedFloor)));
  let loadedRoot;
  try {
    const floor = selectedFloor === "all" ? null : metadata.floors.find(item => String(item.level_index) === selectedFloor);
    if (selectedFloor !== "all" && !floor) throw new Error(`该建筑没有楼层 ${selectedFloor} 的模型`);
    const url = sameOriginURL(floor?.url || metadata.url);
    if (metadata.version != null) url.searchParams.set("v", metadata.version);
    const buffer = await fetchGLB(url, signal, sequence);
    if (sequence !== generation) return;
    $("load-message").textContent = "正在解析三维空间…";
    const gltf = await loader.parseAsync(buffer, new URL(".", url).href);
    loadedRoot = gltf.scene;
    if (sequence !== generation || signal.aborted) { dispose(loadedRoot); loadedRoot = null; if (sequence === generation) throw new Error("模型加载超时，请重试"); return; }
    if (floor && !floor.url) filterFloor(loadedRoot, floor);
    const box = visibleBounds(loadedRoot);
    if (box.isEmpty()) throw new Error("模型没有可显示的几何体");
    const center = box.getCenter(new THREE.Vector3());
    // Both standalone floors and filtered world-space nodes sit on the same y=0 ground.
    loadedRoot.position.x -= center.x;
    loadedRoot.position.z -= center.z;
    loadedRoot.position.y -= box.min.y;
    modelRoot = loadedRoot;
    loadedRoot = null;
    scene.add(modelRoot);
    const size = box.getSize(new THREE.Vector3());
    grid = new THREE.GridHelper(Math.max(size.x, size.z, 10) * 1.5, 24, 0xb7ccdf, 0xdce5ef);
    grid.position.y = -0.02;
    scene.add(grid);
    collectRooms();
    fitCamera();
    $("load-panel").hidden = true;
    viewport.setAttribute("aria-busy", "false");
    $("model-meta").textContent = `${floor?.display_name || (floor ? `${floor.level_index + 1}F` : "整栋模型")} · 版本 ${metadata.version ?? "—"}${metadata.size_bytes ? ` · ${(metadata.size_bytes / 1048576).toFixed(2)} MB` : ""}`;
    $("model-meta").title = metadata.sha256 ? `SHA-256: ${metadata.sha256}` : "";
    notify("ready");
  } catch (error) {
    if (loadedRoot) dispose(loadedRoot);
    if (sequence === generation) status(signal.aborted ? "模型加载超时，请检查网络后重试。" : error.message, true);
  } finally { clearTimeout(timeout); }
}
function roomInfo(object) {
  for (let node = object; node && node !== modelRoot?.parent; node = node.parent) {
    const data = node.userData || {};
    const isRoom = data.room_code != null || data.room_name || data.kind === "room" || data.kind === "facility" || data.category === "room";
    if (isRoom) return { object: node, code: String(data.room_code ?? ""), name: String(data.room_name || data.name || data.display_name || data.room_code || node.name || "未命名房间"), kind: String(data.category || data.kind || "") };
  }
  return null;
}
function collectRooms() {
  const seen = new Set();
  modelRoot.traverse(object => {
    if (!object.isMesh || !visible(object)) return;
    const room = roomInfo(object);
    if (room && !seen.has(room.object.uuid)) { seen.add(room.object.uuid); rooms.push(room); }
  });
  rooms.sort((a, b) => a.name.localeCompare(b.name, "zh-CN", { numeric: true }));
  $("room-select").replaceChildren(new Option(rooms.length ? `选择房间（${rooms.length}）` : "模型未提供房间信息", ""));
  rooms.forEach((room, index) => $("room-select").appendChild(new Option(room.code && room.code !== room.name ? `${room.name} · ${room.code}` : room.name, String(index))));
}
function clearSelection() {
  selectedMaterials.forEach(({ object, original, clones }) => { object.material = original; clones.forEach(material => material.dispose()); });
  selectedMaterials = [];
  if (selection) { scene.remove(selection); dispose(selection); selection = null; }
  $("room-name").textContent = "点击模型中的房间";
  $("room-detail").textContent = "拖动旋转 · 双指或滚轮缩放";
  $("room-select").value = "";
}
function selectRoom(room) {
  clearSelection();
  if (room) {
    room.object.traverse(object => {
      if (!object.isMesh || !visible(object)) return;
      const original = object.material;
      const clones = (Array.isArray(original) ? original : [original]).map(material => {
        const clone = material.clone();
        if (clone.emissive) { clone.emissive.set(0x2563eb); clone.emissiveIntensity = 0.35; }
        else if (clone.color) clone.color.lerp(new THREE.Color(0x2563eb), 0.5);
        return clone;
      });
      object.material = Array.isArray(original) ? clones : clones[0];
      selectedMaterials.push({ object, original, clones });
    });
    selection = new THREE.Box3Helper(visibleBounds(room.object), 0x1e40af);
    scene.add(selection);
    $("room-name").textContent = room.name;
    $("room-detail").textContent = [room.code ? `房间 ${room.code}` : "", room.kind].filter(Boolean).join(" · ") || "已选中房间";
    $("room-select").value = String(rooms.indexOf(room));
  }
  requestRender();
}
function pickRoom(event) {
  if (!modelRoot || !$("load-panel").hidden) return;
  const rect = renderer.domElement.getBoundingClientRect();
  pointer.set((event.clientX - rect.left) / rect.width * 2 - 1, -(event.clientY - rect.top) / rect.height * 2 + 1);
  raycaster.setFromCamera(pointer, camera);
  const hits = raycaster.intersectObject(modelRoot, true).filter(hit => visible(hit.object));
  const info = hits.length ? roomInfo(hits[0].object) : null;
  selectRoom(info ? rooms.find(room => room.object === info.object) : null);
}
function turn(angle) {
  if (!modelRoot) return;
  const offset = camera.position.clone().sub(controls.target);
  offset.applyAxisAngle(new THREE.Vector3(0, 1, 0), angle);
  camera.position.copy(controls.target).add(offset);
  controls.update();
}
function zoom(factor) {
  if (!modelRoot) return;
  const offset = camera.position.clone().sub(controls.target);
  offset.setLength(THREE.MathUtils.clamp(offset.length() * factor, controls.minDistance, controls.maxDistance));
  camera.position.copy(controls.target).add(offset);
  controls.update();
}
async function start() {
  const sequence = ++generation;
  controller?.abort();
  controller = new AbortController();
  const { signal } = controller;
  const timeout = setTimeout(() => controller?.signal === signal && controller.abort(), 20000);
  status("读取建筑模型信息…");
  try {
    if (!buildingId) throw new Error("缺少 building_id，请从建筑详情打开查看器。");
    setupScene();
    clearModel();
    const response = await fetch(`/api/v1/buildings/${encodeURIComponent(buildingId)}/model`, { signal, cache: "no-store", headers: { Accept: "application/json" } });
    let payload;
    try { payload = await response.json(); } catch { throw new Error(`模型服务返回无效响应（HTTP ${response.status}）`); }
    if (!response.ok || (payload.code && payload.code !== "ok")) throw new Error(response.status === 404 ? "该建筑尚未上传模型，请先在管理台上传 GLB。" : payload.message || `HTTP ${response.status}`);
    if (sequence !== generation) return;
    metadata = payload.data || payload;
    metadata.floors = (Array.isArray(metadata.floors) ? metadata.floors : []).filter(floor => floor && Number.isInteger(floor.level_index)).sort((a, b) => a.level_index - b.level_index);
    $("building-title").textContent = metadata.building_name || metadata.name || buildingId;
    $("model-note").hidden = !metadata.estimated;
    $("model-note").textContent = metadata.estimated ? "按平面图估算的测试模型 · 非实测尺寸" : "";
    $("model-note").title = metadata.notes || "";
    document.title = `${$("building-title").textContent} · 三维模型`;
    floorButtons();
    clearTimeout(timeout);
    await loadFloor();
  } catch (error) {
    if (sequence === generation) status(signal.aborted ? "模型服务连接超时，请重试。" : error.message, true);
  } finally { clearTimeout(timeout); }
}
$("retry").addEventListener("click", () => contextLost ? location.reload() : start());
$("reset").addEventListener("click", fitCamera);
$("rotate-left").addEventListener("click", () => turn(-Math.PI / 8));
$("rotate-right").addEventListener("click", () => turn(Math.PI / 8));
$("zoom-in").addEventListener("click", () => zoom(0.8));
$("zoom-out").addEventListener("click", () => zoom(1.25));
$("room-select").addEventListener("change", event => selectRoom(event.target.value === "" ? null : rooms[Number(event.target.value)]));
$("fullscreen").hidden = !document.fullscreenEnabled;
$("fullscreen").addEventListener("click", async () => {
  try { if (document.fullscreenElement) await document.exitFullscreen(); else await document.documentElement.requestFullscreen(); }
  catch { $("room-detail").textContent = "此设备不支持全屏，可继续在当前页面查看。"; }
});
document.addEventListener("fullscreenchange", () => { $("fullscreen").textContent = document.fullscreenElement ? "退出全屏" : "全屏"; });
window.addEventListener("pagehide", () => { generation++; controller?.abort(); });
window.addEventListener("pageshow", event => { if (event.persisted) start(); });
await start();
