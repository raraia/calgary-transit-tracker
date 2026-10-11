// Dashboard logic: fetch stats from the API and draw them. No frameworks, no
// build step; this file is served as-is by the Go server.
"use strict";

const $ = (id) => document.getElementById(id);
const routeSelect = $("route");
const daysSelect = $("days");
const statusEl = $("status");

const pct = (r) => `${Math.round(r * 100)}%`;
const mins = (secs) => {
  const m = secs / 60;
  return `${m >= 0 ? "+" : "−"}${Math.abs(m).toFixed(1)} min`;
};
let lastHours = []; // kept so the chart can be redrawn on resize without refetching
const hourLabel = (h) => (h === 0 ? "12a" : h < 12 ? `${h}a` : h === 12 ? "12p" : `${h - 12}p`);

async function getJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url} returned ${res.status}`);
  return res.json();
}

// Fill the route dropdown, keeping the current choice if it still exists.
async function loadRoutes() {
  const days = daysSelect.value;
  const data = await getJSON(`/api/routes?days=${days}`);
  const w = data.window;
  $("window-note").textContent =
    `On time means no more than ${w.max_early_secs / 60} min early and no more than ${w.max_late_secs / 60} min late.`;

  if (data.routes.length === 0) {
    routeSelect.innerHTML = "<option value=''>No data yet</option>";
    statusEl.textContent = "No settled stop visits yet. Leave the collector running and check back.";
    $("detail").hidden = true;
    return;
  }

  const previous = routeSelect.value;
  // Sort routes the way riders think of them: 1, 2, 3 ... 201, 301, not alphabetically.
  const routes = [...data.routes].sort((a, b) =>
    a.short_name.localeCompare(b.short_name, undefined, { numeric: true }));
  routeSelect.innerHTML = "";
  for (const r of routes) {
    const opt = document.createElement("option");
    opt.value = r.route_id;
    opt.textContent = `${r.short_name}${r.long_name ? " – " + r.long_name : ""} (${pct(r.on_time_ratio)} on time)`;
    opt.dataset.summary = JSON.stringify(r);
    routeSelect.append(opt);
  }
  if (routes.some((r) => r.route_id === previous)) routeSelect.value = previous;
  statusEl.textContent = "";
  await loadRoute();
}

async function loadRoute() {
  const id = routeSelect.value;
  if (!id) return;
  const days = daysSelect.value;
  const summary = JSON.parse(routeSelect.selectedOptions[0].dataset.summary);
  $("t-ontime").textContent = pct(summary.on_time_ratio);
  $("t-delay").textContent = mins(summary.avg_delay_secs);
  $("t-visits").textContent = summary.visits.toLocaleString();

  const enc = encodeURIComponent(id);
  const [hourly, stops] = await Promise.all([
    getJSON(`/api/routes/${enc}/hourly?days=${days}`),
    getJSON(`/api/routes/${enc}/stops?days=${days}&limit=10`),
  ]);
  lastHours = hourly.hours;
  drawChart(lastHours);
  fillHourlyTable(hourly.hours);
  fillStopsTable(stops.stops);
  $("csv-link").href = `/api/routes/${enc}/hourly?days=${days}&format=csv`;
  $("detail").hidden = false;
}

// drawChart renders an SVG column chart: one column per hour, height = on-time share.
function drawChart(hours) {
  const svg = $("chart");
  const W = svg.clientWidth || 800;
  const H = 260;
  const m = { top: 12, right: 8, bottom: 26, left: 40 };
  const innerW = W - m.left - m.right;
  const innerH = H - m.top - m.bottom;
  const slot = innerW / 24;
  const barW = Math.min(24, slot - 2); // thin columns with a gap between them
  const y = (ratio) => m.top + innerH * (1 - ratio);
  const NS = "http://www.w3.org/2000/svg";
  const el = (name, attrs) => {
    const node = document.createElementNS(NS, name);
    for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
    return node;
  };

  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.replaceChildren();

  for (const tick of [0, 0.25, 0.5, 0.75, 1]) {
    svg.append(el("line", { class: "gridline", x1: m.left, x2: W - m.right, y1: y(tick), y2: y(tick) }));
    const label = el("text", { x: m.left - 6, y: y(tick) + 4, "text-anchor": "end" });
    label.textContent = pct(tick);
    svg.append(label);
  }
  for (let h = 0; h < 24; h += 3) {
    const label = el("text", { x: m.left + slot * h + slot / 2, y: H - 8, "text-anchor": "middle" });
    label.textContent = hourLabel(h);
    svg.append(label);
  }

  const tooltip = $("tooltip");
  for (const s of hours) {
    const x = m.left + slot * s.hour + (slot - barW) / 2;
    const top = y(s.on_time_ratio);
    const height = Math.max(0, m.top + innerH - top);
    // Rounded top corners, square at the baseline.
    const r = Math.min(4, barW / 2, height);
    const d = `M${x},${m.top + innerH} V${top + r} Q${x},${top} ${x + r},${top} ` +
      `H${x + barW - r} Q${x + barW},${top} ${x + barW},${top + r} V${m.top + innerH} Z`;

    // An invisible full-height rectangle makes the hover target easy to hit,
    // even for short columns.
    const hit = el("rect", { class: "hit", x: m.left + slot * s.hour, y: m.top, width: slot, height: innerH });
    const bar = el("path", { class: "bar", d });
    hit.addEventListener("mousemove", (ev) => {
      const box = svg.getBoundingClientRect();
      tooltip.textContent = `${hourLabel(s.hour)}: ${pct(s.on_time_ratio)} on time · ${mins(s.avg_delay_secs)} avg · ${s.visits} visits`;
      tooltip.style.left = `${ev.clientX - box.left}px`;
      tooltip.style.top = `${top * (box.height / H)}px`;
      tooltip.hidden = false;
      bar.classList.add("active");
    });
    hit.addEventListener("mouseleave", () => {
      tooltip.hidden = true;
      bar.classList.remove("active");
    });
    svg.append(hit, bar);
  }
}

function fillHourlyTable(hours) {
  const body = $("hourly-table").querySelector("tbody");
  body.replaceChildren(...hours.map((s) =>
    row([hourLabel(s.hour), pct(s.on_time_ratio), mins(s.avg_delay_secs), s.visits.toLocaleString()])));
}

function fillStopsTable(stops) {
  const body = $("stops-table").querySelector("tbody");
  if (stops.length === 0) {
    body.replaceChildren(row(["Not enough data yet", "", "", ""]));
    return;
  }
  body.replaceChildren(...stops.map((s) =>
    row([s.stop_name, mins(s.avg_delay_secs), pct(s.on_time_ratio), s.visits.toLocaleString()])));
}

// row builds a table row. textContent (not innerHTML) means a stop name from
// the data can never be interpreted as HTML.
function row(cells) {
  const tr = document.createElement("tr");
  for (const c of cells) {
    const td = document.createElement("td");
    td.textContent = c;
    tr.append(td);
  }
  return tr;
}

function showError(err) {
  statusEl.textContent = `Couldn't load data: ${err.message}`;
}

routeSelect.addEventListener("change", () => loadRoute().catch(showError));
daysSelect.addEventListener("change", () => loadRoutes().catch(showError));
window.addEventListener("resize", () => { if (!$("detail").hidden) drawChart(lastHours); });
loadRoutes().catch(showError);
