const state = {
  workspace: "ws_1",
  apiToken: localStorage.getItem("mercator.apiToken") || "",
  selectedRunID: "",
  runs: [],
  events: [],
  decision: null,
  connections: [],
  offers: [],
  sinkStatus: null,
  errors: {},
};

const el = {
  workspace: document.querySelector("#workspace"),
  apiToken: document.querySelector("#api-token"),
  summary: document.querySelector("#summary"),
  runs: document.querySelector("#runs"),
  runCount: document.querySelector("#run-count"),
  detail: document.querySelector("#detail"),
  refresh: document.querySelector("#refresh"),
  cancel: document.querySelector("#cancel"),
  nav: document.querySelectorAll(".nav-item"),
};

function workspaceQuery() {
  return `workspace_id=${encodeURIComponent(state.workspace)}`;
}

async function fetchJSON(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (state.apiToken) {
    headers.set("Authorization", `Bearer ${state.apiToken}`);
  }
  const response = await fetch(path, { ...options, headers });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    const error = new Error(data.message || response.statusText);
    error.data = data;
    throw error;
  }
  return data;
}

async function load() {
  state.workspace = el.workspace.value.trim() || "ws_1";
  state.apiToken = el.apiToken.value.trim();
  localStorage.setItem("mercator.apiToken", state.apiToken);
  state.errors = {};
  try {
    const runs = await fetchJSON(`/v1/runs?${workspaceQuery()}`);
    state.runs = runs.runs || [];
    if (!state.selectedRunID && state.runs.length) {
      state.selectedRunID = state.runs[0].id;
    }
  } catch (error) {
    state.errors.runs = error.data?.message || error.message;
    state.runs = [];
  }
  await Promise.all([loadSelectedRun(), loadConnections(), loadOffers(), loadSinkStatus()]);
  render();
}

async function loadSelectedRun() {
  state.events = [];
  state.decision = null;
  if (!state.selectedRunID) return;
  try {
    const events = await fetchJSON(`/v1/runs/${encodeURIComponent(state.selectedRunID)}/events?${workspaceQuery()}`);
    state.events = events.events || [];
  } catch (error) {
    state.errors.events = error.data?.message || error.message;
  }
  try {
    const decision = await fetchJSON(`/v1/runs/${encodeURIComponent(state.selectedRunID)}/decision?${workspaceQuery()}`);
    state.decision = decision.decision;
  } catch (error) {
    state.errors.decision = error.data?.message || error.message;
  }
}

async function loadConnections() {
  try {
    const data = await fetchJSON(`/v1/connections?${workspaceQuery()}`);
    state.connections = data.connections || [];
  } catch (error) {
    state.errors.connections = error.data?.message || error.message;
    state.connections = [];
  }
}

async function loadOffers() {
  try {
    const data = await fetchJSON(`/v1/offers?${workspaceQuery()}`);
    state.offers = data.offers || [];
  } catch (error) {
    state.errors.offers = error.data?.message || error.message;
    state.offers = [];
  }
}

async function loadSinkStatus() {
  try {
    state.sinkStatus = await fetchJSON("/v1/sinks/audit");
  } catch (error) {
    state.errors.sinks = error.data?.message || error.message;
    state.sinkStatus = null;
  }
}

async function cancelSelected() {
  if (!state.selectedRunID) return;
  try {
    await fetchJSON(`/v1/runs/${encodeURIComponent(state.selectedRunID)}:cancel?${workspaceQuery()}`, { method: "POST" });
    await load();
  } catch (error) {
    state.errors.cancel = error.data?.message || error.message;
    render();
  }
}

function render() {
  el.summary.textContent = `${state.runs.length} runs, ${state.offers.length} offers, ${state.connections.length} connections`;
  el.runCount.textContent = String(state.runs.length);
  renderRuns();
  renderDetail();
}

function renderRuns() {
  if (state.errors.runs) {
    el.runs.innerHTML = `<div class="error">${escapeHTML(state.errors.runs)}</div>`;
    return;
  }
  if (!state.runs.length) {
    el.runs.innerHTML = `<div class="empty">No runs found for ${escapeHTML(state.workspace)}.</div>`;
    return;
  }
  el.runs.innerHTML = state.runs.map((run) => `
    <button class="run-row ${run.id === state.selectedRunID ? "active" : ""}" data-run-id="${escapeHTML(run.id)}">
      <span>
        <span class="run-id">${escapeHTML(run.id)}</span>
        <span class="run-meta">${escapeHTML(run.workload_revision_id || "workload revision pending")}</span>
      </span>
      <span class="status ${escapeHTML((run.phase || "").toLowerCase())}">${escapeHTML(run.phase || "unknown")}</span>
    </button>
  `).join("");
  el.runs.querySelectorAll(".run-row").forEach((row) => {
    row.addEventListener("click", async () => {
      state.selectedRunID = row.dataset.runId;
      await loadSelectedRun();
      render();
    });
  });
}

function renderDetail() {
  const selected = state.runs.find((run) => run.id === state.selectedRunID);
  if (!selected) {
    el.detail.innerHTML = `<div class="empty">Select a run to inspect lifecycle, events, decisions, offers, connections, and sink status.</div>`;
    return;
  }
  el.detail.innerHTML = `
    ${panel("Run Detail", metrics([
      ["Run", selected.id],
      ["Phase", selected.phase || "unknown"],
      ["Workspace", state.workspace],
      ["Revision", selected.workload_revision_id || "not recorded"],
    ]))}
    ${panel("Placement Decision", renderDecision())}
    ${panel("Events", renderEvents(), "wide")}
    ${panel("Connections", renderList(state.connections, "No connections configured.", (item) => `${item.id} · ${item.adapter_type} · ${item.authorized ? "authorized" : "pending"}`))}
    ${panel("Offers", renderList(state.offers, "No offers cached.", (item) => `${item.id} · ${item.adapter_type} · ${item.platform?.os || "?"}/${item.platform?.architecture || "?"}`))}
    ${panel("Sink Status", renderSink())}
    ${state.errors.cancel ? `<div class="error">${escapeHTML(state.errors.cancel)}</div>` : ""}
  `;
}

function panel(title, body, extra = "") {
  return `<section class="panel ${extra}"><h3>${escapeHTML(title)}</h3>${body}</section>`;
}

function metrics(rows) {
  return `<div class="metric-list">${rows.map(([label, value]) => `
    <div class="metric"><span>${escapeHTML(label)}</span><strong>${escapeHTML(String(value))}</strong></div>
  `).join("")}</div>`;
}

function renderEvents() {
  if (state.errors.events) return `<div class="error">${escapeHTML(state.errors.events)}</div>`;
  if (!state.events.length) return `<div class="empty">No public events recorded.</div>`;
  return `<div class="timeline">${state.events.slice(-8).map((event) => `
    <div class="event"><strong>${escapeHTML(event.type)}</strong>${escapeHTML(event.time || `position ${event.globalposition}`)}</div>
  `).join("")}</div>`;
}

function renderDecision() {
  if (state.errors.decision) return `<div class="error">${escapeHTML(state.errors.decision)}</div>`;
  if (!state.decision) return `<div class="empty">No placement decision found.</div>`;
  const selected = state.decision.selected_candidate_id || "not selected";
  const candidates = Array.isArray(state.decision.candidates) ? state.decision.candidates.length : 0;
  return metrics([
    ["Decision", state.decision.id || "unknown"],
    ["Selected", selected],
    ["Candidates", candidates],
    ["Model", state.decision.model_version || "unknown"],
  ]);
}

function renderList(items, empty, formatter) {
  if (!items.length) return `<div class="empty">${escapeHTML(empty)}</div>`;
  return `<div class="simple-list">${items.map((item) => `<div class="metric"><span>${escapeHTML(formatter(item))}</span></div>`).join("")}</div>`;
}

function renderSink() {
  if (state.errors.sinks) return `<div class="error">${escapeHTML(state.errors.sinks)}</div>`;
  if (!state.sinkStatus) return `<div class="empty">No sink status loaded.</div>`;
  return metrics([
    ["Sink", state.sinkStatus.sink_id],
    ["Cursor", state.sinkStatus.cursor],
    ["Has cursor", state.sinkStatus.has_cursor ? "yes" : "no"],
  ]);
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[char]);
}

el.refresh.addEventListener("click", load);
el.cancel.addEventListener("click", cancelSelected);
el.apiToken.value = state.apiToken;
el.apiToken.addEventListener("change", load);
el.workspace.addEventListener("change", () => {
  state.selectedRunID = "";
  load();
});
el.nav.forEach((button) => {
  button.addEventListener("click", () => {
    el.nav.forEach((item) => item.classList.remove("active"));
    button.classList.add("active");
  });
});

load();
