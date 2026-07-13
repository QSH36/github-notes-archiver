"use strict";

const state = {
  csrfToken: "",
  repositories: [],
  discovered: new Map(),
  selected: new Set(),
  previews: new Map(),
};

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const writeMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

function valueOf(object, keys, fallback = "") {
  for (const key of keys) {
    if (object && object[key] !== undefined && object[key] !== null) return object[key];
  }
  return fallback;
}

function arrayOf(payload, keys) {
  if (Array.isArray(payload)) return payload;
  for (const key of keys) if (Array.isArray(payload?.[key])) return payload[key];
  if (payload?.data) return arrayOf(payload.data, keys);
  return [];
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>'"]/g, character => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;",
  })[character]);
}

function formatTime(value) {
  if (!value) return "尚无记录";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? String(value) : new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium", timeStyle: "short",
  }).format(date);
}

function statusClass(value) {
  const status = String(value || "").toLowerCase();
  if (["ok", "healthy", "enabled", "success", "synced", "completed", "active"].some(item => status.includes(item))) return "success";
  if (["error", "failed", "invalid", "paused", "unhealthy"].some(item => status.includes(item))) return "error";
  if (["pending", "queued", "warning", "retry", "processing"].some(item => status.includes(item))) return "warning";
  return "";
}

function statusText(value) {
  const status = String(value || "unknown").toLowerCase();
  const labels = { ok: "正常", healthy: "正常", enabled: "已启用", active: "已启用", pending: "等待中", queued: "排队中", processing: "处理中", synced: "已同步", completed: "已完成", warning: "警告", paused: "已暂停", error: "错误", failed: "失败", unknown: "未知" };
  return labels[status] || String(value || "未知");
}

async function api(path, options = {}) {
  const method = (options.method || "GET").toUpperCase();
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  if (writeMethods.has(method) && state.csrfToken) headers.set("X-CSRF-Token", state.csrfToken);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const response = await fetch(path, { ...options, method, headers, credentials: "same-origin" });
  const responseCSRF = response.headers.get("X-CSRF-Token");
  const contentType = response.headers.get("Content-Type") || "";
  const payload = response.status === 204 ? null : contentType.includes("application/json") ? await response.json() : await response.text();
  const bodyCSRF = valueOf(payload, ["csrf_token", "csrfToken"], "");
  if (responseCSRF || bodyCSRF) state.csrfToken = responseCSRF || bodyCSRF;
  if (!response.ok) {
    const error = payload?.error;
    const message = typeof error === "string" ? error : valueOf(error, ["message", "code"], valueOf(payload, ["message"], `请求失败（${response.status}）`));
    const apiError = new Error(message);
    apiError.status = response.status;
    apiError.details = error?.details;
    throw apiError;
  }
  return payload;
}

function setInlineStatus(element, message, error = false) {
  element.textContent = message;
  element.classList.toggle("error", error);
}

function toast(message, error = false) {
  const item = document.createElement("div");
  item.className = `toast${error ? " error" : ""}`;
  item.textContent = message;
  $("#toastRegion").append(item);
  window.setTimeout(() => item.remove(), 5000);
}

function showLogin() {
  $("#appView").hidden = true;
  $("#loginView").hidden = false;
  window.setTimeout(() => $("#adminToken").focus(), 0);
}

function showApp() {
  $("#loginView").hidden = true;
  $("#appView").hidden = false;
  navigate(location.hash.slice(1) || "dashboard");
}

function navigate(route) {
  const known = new Set($$("[data-route]").map(page => page.dataset.route));
  const target = known.has(route) ? route : "dashboard";
  $$("[data-route]").forEach(page => { page.hidden = page.dataset.route !== target; });
  $$("[data-route-link]").forEach(link => {
    if (link.dataset.routeLink === target) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  $("#primaryNav").classList.remove("open");
  $("#navToggle").setAttribute("aria-expanded", "false");
  document.title = `${$("[data-route]:not([hidden]) h1")?.textContent || "GitHub 笔记归档"} · 笔记归档`;
  $("#mainContent").focus({ preventScroll: true });
  if (target === "dashboard") loadDashboard();
  if (target === "repositories") loadRepositories();
  if (target === "notes" || target === "imports") loadRepositories();
  if (target === "operations") loadOperations();
}

async function bootstrap() {
  try {
    await api("/api/v1/status");
    showApp();
  } catch (error) {
    if (error.status === 401 || error.status === 403) showLogin();
    else showLogin();
  }
}

async function login(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const button = $("button[type=submit]", form);
  const errorBox = $("#loginError");
  errorBox.textContent = "";
  button.disabled = true;
  try {
    await api("/api/v1/session", { method: "POST", body: JSON.stringify({ token: form.elements.token.value }) });
    form.reset();
    showApp();
  } catch (error) {
    errorBox.textContent = error.message || "登录失败，请检查管理令牌。";
  } finally {
    button.disabled = false;
  }
}

async function logout() {
  try { await api("/api/v1/session", { method: "DELETE" }); } catch (_) { /* 会话已失效时仍返回登录页。 */ }
  state.csrfToken = "";
  state.discovered.clear();
  state.selected.clear();
  showLogin();
}

function normalizeRepository(repo, discoverySessionID = "") {
  const permissions = repo.permissions || {};
  const privateRepo = valueOf(repo, ["private", "is_private"], repo.visibility === "private");
  const fullName = valueOf(repo, ["full_name", "fullName", "name"], "未命名仓库");
  const owner = typeof repo.owner === "string" ? repo.owner : valueOf(repo.owner, ["login", "name"], fullName.split("/")[0]);
  const enabled = Boolean(valueOf(repo, ["enabled", "is_enabled"], false));
  const admin = Boolean(valueOf(repo, ["can_admin", "admin"], permissions.admin));
  const push = Boolean(valueOf(repo, ["can_push", "push"], permissions.push));
  const disabledReason = valueOf(repo, ["disabled_reason", "ineligible_reason", "activation_error"], "");
  const githubEligible = valueOf(repo, ["eligible"], undefined);
  const eligible = !enabled && (githubEligible === undefined
    ? privateRepo && !repo.fork && !repo.archived && admin && push && !disabledReason
    : Boolean(githubEligible));
  return {
    ...repo,
    id: String(valueOf(repo, ["id", "node_id", "full_name"], fullName)),
    full_name: fullName,
    owner,
    private: Boolean(privateRepo),
    default_branch: valueOf(repo, ["default_branch", "defaultBranch"], "—"),
    enabled,
    eligible,
    health: valueOf(repo, ["health", "status"], enabled ? "enabled" : "unknown"),
    last_sync: valueOf(repo, ["last_sync", "lastSync"], ""),
    disabled_reason: disabledReason || valueOf(repo, ["disabled_why"], "") || (!privateRepo ? "公开仓库" : repo.fork ? "Fork 仓库" : repo.archived ? "已归档" : !admin ? "缺少管理权限" : githubEligible === false ? "仓库策略或权限不允许" : !push ? "默认分支不可写" : ""),
    discovery_session_id: discoverySessionID || valueOf(repo, ["discovery_session_id", "discoverySessionId"], ""),
  };
}

async function discoverRepositories(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const status = $("#discoveryStatus");
  const submit = $("button[type=submit]", form);
  setInlineStatus(status, "正在建立临时发现会话…");
  submit.disabled = true;
  try {
    await api("/api/v1/config", {
      method: "PUT",
      body: JSON.stringify({
        author_name: form.elements.author_name.value.trim(),
        author_email: form.elements.author_email.value.trim(),
      }),
    });
    const payload = await api("/api/v1/github/discovery-sessions", {
      method: "POST",
      body: JSON.stringify({
        username: form.elements.username.value.trim(),
        owner: form.elements.resource_owner.value.trim(),
        token: form.elements.token.value,
      }),
    });
    const sessionID = String(valueOf(payload, ["id", "discovery_session_id", "session_id"], valueOf(payload?.data, ["id"], "")));
    if (!sessionID) throw new Error("服务器未返回发现会话 ID。请检查 API 配置。");
    form.elements.token.value = "";
    const repositoriesPayload = await api(`/api/v1/github/discovery-sessions/${encodeURIComponent(sessionID)}/repositories`);
    const repositories = arrayOf(repositoriesPayload, ["repositories", "items"]).map(repo => normalizeRepository(repo, sessionID));
    repositories.forEach(repo => state.discovered.set(`${sessionID}:${repo.id}`, repo));
    setInlineStatus(status, `已获取 ${repositories.length} 个仓库，令牌输入已清空。`);
    toast(`已从 ${form.elements.resource_owner.value.trim()} 获取 ${repositories.length} 个仓库`);
    location.hash = "repositories";
    renderRepositoryTable();
  } catch (error) {
    form.elements.token.value = "";
    setInlineStatus(status, error.message, true);
  } finally {
    submit.disabled = false;
  }
}

async function addManualRepository(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const status = $("#manualStatus");
  const submit = $("button[type=submit]", form);
  setInlineStatus(status, "正在验证私钥与仓库访问权限…");
  submit.disabled = true;
  try {
    const privateKey = await form.elements.private_key.files[0].text();
    const cloneURL = form.elements.ssh_url.value.trim();
    const match = cloneURL.match(/^git@github\.com:([^/]+)\/([^/]+?)(?:\.git)?$/i);
    if (!match) throw new Error("仓库地址必须使用 git@github.com:owner/repository.git 格式。 ");
    await api("/api/v1/repositories/manual", {
      method: "POST",
      body: JSON.stringify({
        full_name: `${match[1]}/${match[2]}`,
        clone_url: cloneURL,
        default_branch: form.elements.default_branch.value.trim(),
        private_key: privateKey,
		author_name: form.elements.author_name.value.trim(),
		author_email: form.elements.author_email.value.trim(),
      }),
    });
    form.reset();
    setInlineStatus(status, "仓库已验证并添加。私钥内容不会回显。 ");
    toast("手工仓库已添加");
    await loadRepositories();
  } catch (error) {
    form.elements.private_key.value = "";
    setInlineStatus(status, error.message, true);
  } finally {
    submit.disabled = false;
  }
}

async function loadRepositories() {
  try {
    const payload = await api("/api/v1/repositories");
    state.repositories = arrayOf(payload, ["repositories", "items"]).map(repo => normalizeRepository(repo));
    updateRepositorySelects();
    renderRepositoryTable();
  } catch (error) {
    if (error.status === 401) return showLogin();
    $("#repositoryTableStatus").textContent = `加载已启用仓库失败：${error.message}`;
  }
}

function mergedRepositories() {
  const rows = new Map();
  for (const repository of state.discovered.values()) rows.set(`${repository.discovery_session_id}:${repository.id}`, repository);
  for (const repository of state.repositories) {
    const matchingKey = [...rows.keys()].find(key => rows.get(key).full_name === repository.full_name);
    if (matchingKey) rows.set(matchingKey, { ...rows.get(matchingKey), ...repository, enabled: true, eligible: false });
    else rows.set(`active:${repository.id}`, repository);
  }
  return [...rows.values()];
}

function filteredRepositories() {
  const query = $("#repositorySearch").value.trim().toLocaleLowerCase();
  const filter = $("#repositoryFilter").value;
  return mergedRepositories().filter(repo => {
    if (query && !`${repo.full_name} ${repo.owner}`.toLocaleLowerCase().includes(query)) return false;
    if (filter === "eligible") return repo.eligible;
    if (filter === "enabled") return repo.enabled;
    if (filter === "unavailable") return !repo.eligible && !repo.enabled;
    return true;
  });
}

function repositoryKey(repo) {
  return `${repo.discovery_session_id}:${repo.id}`;
}

function renderRepositoryTable() {
  const rows = filteredRepositories();
  const body = $("#repositoryTableBody");
  if (!rows.length) {
    body.innerHTML = '<tr><td colspan="7" class="empty-cell">没有符合当前条件的仓库。</td></tr>';
  } else {
    body.innerHTML = rows.map(repo => {
      const key = repositoryKey(repo);
      const checked = state.selected.has(key);
      const type = repo.private ? (repo.fork ? "私有 Fork" : "私有") : "公开";
      const permission = repo.eligible || repo.enabled ? "管理 + 写入" : repo.disabled_reason || "权限不足";
      const status = repo.enabled ? repo.health : repo.eligible ? "可启用" : "不可启用";
      const statusKind = repo.enabled ? statusClass(repo.health) : repo.eligible ? "success" : "warning";
      return `<tr>
        <td class="check-cell"><input type="checkbox" data-repository-key="${escapeHTML(key)}" aria-label="选择 ${escapeHTML(repo.full_name)}" ${repo.eligible ? "" : "disabled"} ${checked ? "checked" : ""}></td>
        <td><strong>${escapeHTML(repo.full_name)}</strong><small>${escapeHTML(repo.owner)}</small></td>
        <td>${escapeHTML(type)}${repo.archived ? " · 已归档" : ""}</td>
        <td>${escapeHTML(repo.default_branch)}</td>
        <td>${escapeHTML(permission)}</td>
        <td><span class="status-badge ${statusKind}">${escapeHTML(statusText(status))}</span>${repo.disabled_reason ? `<small>${escapeHTML(repo.disabled_reason)}</small>` : ""}</td>
        <td>${repo.enabled ? `<button class="quiet-button compact" type="button" data-test-repository="${escapeHTML(repo.id)}">检测</button>` : ""}</td>
      </tr>`;
    }).join("");
  }
  const eligibleVisible = rows.filter(repo => repo.eligible);
  const selectedVisible = eligibleVisible.filter(repo => state.selected.has(repositoryKey(repo))).length;
  const allBox = $("#selectAllRepositories");
  allBox.checked = eligibleVisible.length > 0 && selectedVisible === eligibleVisible.length;
  allBox.indeterminate = selectedVisible > 0 && selectedVisible < eligibleVisible.length;
  allBox.disabled = eligibleVisible.length === 0;
  updateSelectionBar();
  $("#repositoryTableStatus").textContent = `显示 ${rows.length} 个仓库，其中 ${eligibleVisible.length} 个可启用。`;
}

function updateSelectionBar() {
  $("#selectedCount").textContent = state.selected.size;
  $("#selectionBar").hidden = state.selected.size === 0;
}

function toggleRepositorySelection(event) {
  const input = event.target.closest("[data-repository-key]");
  if (!input) return;
  if (input.checked) state.selected.add(input.dataset.repositoryKey);
  else state.selected.delete(input.dataset.repositoryKey);
  renderRepositoryTable();
}

function toggleAllRepositories(event) {
  for (const repo of filteredRepositories().filter(item => item.eligible)) {
    const key = repositoryKey(repo);
    if (event.currentTarget.checked) state.selected.add(key);
    else state.selected.delete(key);
  }
  renderRepositoryTable();
}

function openActivationDialog() {
  const repositories = mergedRepositories().filter(repo => state.selected.has(repositoryKey(repo)) && repo.eligible);
  if (!repositories.length) return;
  $("#activationRepositoryList").innerHTML = repositories.map(repo => `<div>${escapeHTML(repo.full_name)}</div>`).join("");
  $("#patLifecycleAcknowledgement").checked = false;
  $("#confirmActivationButton").disabled = true;
  $("#activationDialog").showModal();
}

async function activateSelected(event) {
  event.preventDefault();
  if (!$("#patLifecycleAcknowledgement").checked) return;
  const button = $("#confirmActivationButton");
  const selectedRepositories = mergedRepositories().filter(repo => state.selected.has(repositoryKey(repo)) && repo.eligible);
  const groups = selectedRepositories.reduce((map, repo) => {
    if (!map.has(repo.discovery_session_id)) map.set(repo.discovery_session_id, []);
    map.get(repo.discovery_session_id).push(repo);
    return map;
  }, new Map());
  button.disabled = true;
  button.textContent = "正在启用…";
  let successCount = 0;
  const failures = [];
  for (const [sessionID, repositories] of groups) {
    try {
      const payload = await api("/api/v1/repositories/activations", {
        method: "POST",
        body: JSON.stringify({
          discovery_id: sessionID,
          repository_ids: repositories.map(repo => Number(repo.id)),
          acknowledge_pat_lifecycle: true,
        }),
      });
      const results = arrayOf(payload, ["results", "repositories"]);
      if (results.length) {
        for (const result of results) {
          if (valueOf(result, ["success", "enabled"], false)) successCount++;
          else failures.push(`${valueOf(result, ["full_name", "repository", "repository_id"], "仓库")}: ${valueOf(result, ["error", "message"], "启用失败")}`);
        }
      } else successCount += repositories.length;
    } catch (error) {
      repositories.forEach(repo => failures.push(`${repo.full_name}: ${error.message}`));
    }
  }
  state.selected.clear();
  $("#activationDialog").close();
  button.textContent = "确认并启用";
  toast(`已启用 ${successCount} 个仓库${failures.length ? `，${failures.length} 个失败` : ""}`, failures.length > 0 && successCount === 0);
  if (failures.length) $("#repositoryTableStatus").textContent = failures.join("；");
  await loadRepositories();
}

async function testRepository(repositoryID, button) {
  button.disabled = true;
  try {
    const payload = await api(`/api/v1/repositories/${encodeURIComponent(repositoryID)}/test`, { method: "POST", body: "{}" });
    toast(valueOf(payload, ["message"], "仓库连接正常"));
  } catch (error) {
    toast(`仓库检测失败：${error.message}`, true);
  } finally {
    button.disabled = false;
  }
}

function updateRepositorySelects() {
  const enabled = state.repositories.filter(repo => repo.enabled !== false);
  const options = enabled.map(repo => `<option value="${escapeHTML(repo.id)}">${escapeHTML(repo.full_name)}</option>`).join("");
  for (const id of ["noteRepository", "importRepository"]) {
    const select = $(`#${id}`);
    const current = select.value;
    select.innerHTML = `<option value="">请选择一个已启用仓库</option>${options}`;
    if (enabled.some(repo => repo.id === current)) select.value = current;
  }
  const filter = $("#eventRepositoryFilter");
  const currentFilter = filter.value;
  filter.innerHTML = `<option value="">全部仓库</option>${options}`;
  if (enabled.some(repo => repo.id === currentFilter)) filter.value = currentFilter;
}

async function saveNote(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const status = $("#noteStatus");
  const submit = $("button[type=submit]", form);
  const path = form.elements.logical_path.value.trim();
  if (path.startsWith("/") || path.split("/").includes("..") || path.includes("\\")) {
    setInlineStatus(status, "路径必须是安全的相对路径，不能包含 ..、反斜杠或绝对路径。", true);
    return;
  }
  setInlineStatus(status, "正在保存不可变版本…");
  submit.disabled = true;
  try {
    await api("/api/v1/notes/versions", {
      method: "POST",
      body: JSON.stringify({
        repository_id: form.elements.repository_id.value,
        logical_path: path,
        title: form.elements.title.value.trim(),
        content: form.elements.content.value,
        source: "gui",
      }),
    });
    const repositoryID = form.elements.repository_id.value;
    form.reset();
    form.elements.repository_id.value = repositoryID;
    $("#noteCharacterCount").textContent = "0 字符";
    setInlineStatus(status, "版本已保存并加入同步队列。 ");
    toast("真实笔记版本已加入队列");
  } catch (error) {
    setInlineStatus(status, error.message, true);
  } finally {
    submit.disabled = false;
  }
}

async function previewImport(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const status = $("#importStatus");
  const submit = $("button[type=submit]", form);
  const body = new FormData(form);
  setInlineStatus(status, "正在校验文件、哈希、路径和远端 HEAD…");
  submit.disabled = true;
  try {
    const payload = await api("/api/v1/imports/previews", { method: "POST", body });
    const preview = payload?.preview || payload?.data || payload;
    const previewID = String(valueOf(preview, ["id", "preview_id"], ""));
    if (!previewID) throw new Error("服务器未返回预览 ID。 ");
    state.previews.set(previewID, preview);
    renderImportPreview(preview);
    setInlineStatus(status, "安全预览已生成。 ");
  } catch (error) {
    $("#importPreview").textContent = `无法生成预览：${error.message}`;
    setInlineStatus(status, error.message, true);
  } finally {
    submit.disabled = false;
  }
}

function renderImportPreview(preview) {
  const id = String(valueOf(preview, ["id", "preview_id"], ""));
  const count = valueOf(preview, ["version_count", "count"], arrayOf(preview, ["rows", "versions"]).length);
  const repository = valueOf(preview, ["repository_full_name", "repository"], $("#importRepository").selectedOptions[0]?.textContent || "目标仓库");
  const warnings = arrayOf(preview, ["warnings"]);
  $("#importPreview").innerHTML = `
    <div class="repo-health-row"><div><strong>${escapeHTML(repository)}</strong><small>基准 HEAD：${escapeHTML(valueOf(preview, ["base_head"], "—"))}</small></div><span class="status-badge success">预览有效</span></div>
    <div class="repo-health-row"><div><strong>${escapeHTML(count)} 个真实版本</strong><small>${escapeHTML(valueOf(preview, ["date_from", "from"], "—"))} 至 ${escapeHTML(valueOf(preview, ["date_to", "to"], "—"))}</small></div></div>
    ${warnings.length ? `<div class="callout warning">${warnings.map(escapeHTML).join("；")}</div>` : ""}
    <button class="primary-button full" type="button" data-run-import="${escapeHTML(id)}">确认并加入导入队列</button>`;
}

async function runImport(previewID, button) {
  button.disabled = true;
  button.textContent = "正在确认…";
  try {
	const preview = state.previews.get(previewID);
	if (!preview) throw new Error("导入预览已丢失，请重新生成。");
	await api("/api/v1/imports/runs", {
	  method: "POST",
	  body: JSON.stringify({
		preview_id: previewID,
		package_sha256: valueOf(preview, ["package_sha256"], ""),
		confirm: true,
	  }),
	});
    $("#importPreview").textContent = "导入批次已加入队列。若远端 HEAD 已变化，服务器会拒绝执行并要求重新预览。";
    toast("历史版本已加入导入队列");
  } catch (error) {
    toast(`确认导入失败：${error.message}`, true);
    button.disabled = false;
    button.textContent = "确认并加入导入队列";
  }
}

async function syncAll() {
  const button = $("#syncAllButton");
  button.disabled = true;
  try {
    await api("/api/v1/sync", { method: "POST", body: "{}" });
    toast("已请求同步全部启用仓库");
    await loadDashboard();
  } catch (error) {
    toast(`同步请求失败：${error.message}`, true);
  } finally {
    button.disabled = false;
  }
}

async function loadDashboard() {
  const settled = await Promise.allSettled([
    api("/healthz"), api("/api/v1/repositories"), api("/api/v1/queue"), api("/api/v1/events?limit=6"),
  ]);
  const [healthResult, repoResult, queueResult, eventResult] = settled;
  if (repoResult.status === "fulfilled") state.repositories = arrayOf(repoResult.value, ["repositories", "items"]).map(repo => normalizeRepository(repo));
  const queues = queueResult.status === "fulfilled" ? arrayOf(queueResult.value, ["queue", "items", "events"]) : [];
  const events = eventResult.status === "fulfilled" ? arrayOf(eventResult.value, ["events", "items"]) : [];
  $("#metricRepos").textContent = state.repositories.filter(repo => repo.enabled !== false).length;
  $("#metricQueue").textContent = queues.filter(item => !["completed", "synced"].includes(String(item.status).toLowerCase())).length;
  $("#metricErrors").textContent = state.repositories.filter(repo => statusClass(repo.health) === "error").length;
  if (healthResult.status === "fulfilled") {
    $("#metricHealth").textContent = statusText(valueOf(healthResult.value, ["status", "health"], "ok"));
    $("#healthDetail").textContent = valueOf(healthResult.value, ["message", "version"], "服务响应正常");
  } else {
    $("#metricHealth").textContent = "连接异常";
    $("#healthDetail").textContent = healthResult.reason.message;
  }
  renderHealthRows(state.repositories, $("#dashboardRepos"));
  renderEventRows(events, $("#dashboardEvents"));
  updateRepositorySelects();
}

function renderHealthRows(repositories, container) {
  if (!repositories.length) return container.textContent = "尚未启用仓库。请先连接 GitHub。";
  container.innerHTML = repositories.slice(0, 6).map(repo => `<div class="repo-health-row"><div><strong>${escapeHTML(repo.full_name)}</strong><small>上次同步：${escapeHTML(formatTime(repo.last_sync))}</small></div><span class="status-badge ${statusClass(repo.health)}">${escapeHTML(statusText(repo.health))}</span></div>`).join("");
}

function renderQueueRows(items, container) {
  if (!items.length) return container.textContent = "当前队列为空。";
  container.innerHTML = items.map(item => `<div class="queue-row"><div><strong>${escapeHTML(valueOf(item, ["title", "logical_path", "type"], "版本任务"))}</strong><small>${escapeHTML(valueOf(item, ["repository_full_name", "repository"], "未知仓库"))} · ${escapeHTML(formatTime(valueOf(item, ["version_at", "created_at"], "")))}</small></div><span class="status-badge ${statusClass(item.status)}">${escapeHTML(statusText(item.status))}</span></div>`).join("");
}

function renderEventRows(items, container) {
  if (!items.length) return container.textContent = "尚无事件记录。";
  container.innerHTML = items.map(item => `<div class="event-row"><div><strong>${escapeHTML(valueOf(item, ["message", "action", "type"], "系统事件"))}</strong><small>${escapeHTML(valueOf(item, ["repository_full_name", "repository"], "系统"))} · ${escapeHTML(formatTime(valueOf(item, ["created_at", "time", "timestamp"], "")))}</small></div><span class="status-badge ${statusClass(valueOf(item, ["level", "status"], "info"))}">${escapeHTML(statusText(valueOf(item, ["level", "status"], "info")))}</span></div>`).join("");
}

async function loadOperations() {
  const params = new URLSearchParams();
  const level = $("#eventLevelFilter").value;
  const repositoryID = $("#eventRepositoryFilter").value;
  if (level) params.set("level", level);
  if (repositoryID) params.set("repository_id", repositoryID);
  const [queueResult, eventResult] = await Promise.allSettled([
    api("/api/v1/queue"), api(`/api/v1/events${params.size ? `?${params}` : ""}`),
  ]);
  if (queueResult.status === "fulfilled") renderQueueRows(arrayOf(queueResult.value, ["queue", "items", "events"]), $("#queueList"));
  else $("#queueList").textContent = `队列加载失败：${queueResult.reason.message}`;
  if (eventResult.status === "fulfilled") renderEventRows(arrayOf(eventResult.value, ["events", "items"]), $("#eventList"));
  else $("#eventList").textContent = `事件加载失败：${eventResult.reason.message}`;
}

function activateTab(tab) {
  const tabs = $$("[role=tab]");
  tabs.forEach(item => {
    const active = item === tab;
    item.setAttribute("aria-selected", String(active));
    item.tabIndex = active ? 0 : -1;
    $(`#${item.getAttribute("aria-controls")}`).hidden = !active;
  });
  tab.focus();
}

function bindEvents() {
  window.addEventListener("hashchange", () => navigate(location.hash.slice(1)));
  $("#loginForm").addEventListener("submit", login);
  $("#logoutButton").addEventListener("click", logout);
  $("#discoveryForm").addEventListener("submit", discoverRepositories);
  $("#manualRepositoryForm").addEventListener("submit", addManualRepository);
  $("#noteForm").addEventListener("submit", saveNote);
  $("#importPreviewForm").addEventListener("submit", previewImport);
  $("#syncAllButton").addEventListener("click", syncAll);
  $("#refreshRepositoriesButton").addEventListener("click", loadRepositories);
  $("#refreshOperationsButton").addEventListener("click", loadOperations);
  $("#eventLevelFilter").addEventListener("change", loadOperations);
  $("#eventRepositoryFilter").addEventListener("change", loadOperations);
  $("#repositorySearchForm").addEventListener("submit", event => { event.preventDefault(); renderRepositoryTable(); });
  $("#repositorySearch").addEventListener("input", renderRepositoryTable);
  $("#repositoryFilter").addEventListener("change", renderRepositoryTable);
  $("#repositoryTableBody").addEventListener("change", toggleRepositorySelection);
  $("#repositoryTableBody").addEventListener("click", event => {
    const button = event.target.closest("[data-test-repository]");
    if (button) testRepository(button.dataset.testRepository, button);
  });
  $("#selectAllRepositories").addEventListener("change", toggleAllRepositories);
  $("#activateSelectedButton").addEventListener("click", openActivationDialog);
  $("#patLifecycleAcknowledgement").addEventListener("change", event => { $("#confirmActivationButton").disabled = !event.currentTarget.checked; });
  $("#activationDialog").addEventListener("close", () => { $("#confirmActivationButton").disabled = true; });
  $("#confirmActivationButton").addEventListener("click", activateSelected);
  $("#importPreview").addEventListener("click", event => {
    const button = event.target.closest("[data-run-import]");
    if (button) runImport(button.dataset.runImport, button);
  });
  $("#noteContent").addEventListener("input", event => { $("#noteCharacterCount").textContent = `${event.currentTarget.value.length} 字符`; });
  $("#navToggle").addEventListener("click", event => {
    const open = $("#primaryNav").classList.toggle("open");
    event.currentTarget.setAttribute("aria-expanded", String(open));
  });
  $$('[data-toggle-password]').forEach(button => button.addEventListener("click", () => {
    const input = $(`#${button.dataset.togglePassword}`);
    const reveal = input.type === "password";
    input.type = reveal ? "text" : "password";
    button.textContent = reveal ? "隐藏" : "显示";
  }));
  $$('[role=tab]').forEach(tab => {
    tab.addEventListener("click", () => activateTab(tab));
    tab.addEventListener("keydown", event => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const tabs = $$("[role=tab]");
      const index = tabs.indexOf(tab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : event.key === "ArrowRight" ? (index + 1) % tabs.length : (index - 1 + tabs.length) % tabs.length;
      activateTab(tabs[next]);
    });
  });
}

bindEvents();
bootstrap();
