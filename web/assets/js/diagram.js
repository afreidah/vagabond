// -------------------------------------------------------------------------------
// Interactive Diagrams
//
// Author: Alex Freidah
//
// Renders the flowchart a page declares in window.vagabondDiagram - its mermaid
// source and the detail shown for each node - and wires hover tooltips and
// highlighting of every path through the hovered node. Every colour comes from
// the --dg-* variables in diagrams.css, so the diagrams follow the site's
// palette and re-render when the theme toggles.
// -------------------------------------------------------------------------------

import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@11.8.0/dist/mermaid.esm.min.mjs";
import elkLayouts from "https://cdn.jsdelivr.net/npm/@mermaid-js/layout-elk@0.1.8/dist/mermaid-layout-elk.esm.min.mjs";

mermaid.registerLayoutLoaders(elkLayouts);

// Node class names by role. Pages assign the names with ":::name"; several
// share a role, whose colours are the --dg-<role>-* variables in diagrams.css,
// so the nodes, the badges and the legend swatches are coloured from one place.
const ROLES = {
  entry: "entry",
  success: "success", data: "success",
  process: "process",
  decision: "decision",
  storage: "storage",
  filter: "filter", check: "filter",
  reject: "reject",
  background: "background",
};

const BOLD = new Set(["entry", "success", "reject"]);

const isDark = () => document.documentElement.classList.contains("dark");

// palette reads one --dg-* variable as the current theme defines it.
const palette = (name) => getComputedStyle(document.documentElement).getPropertyValue(`--dg-${name}`).trim();

// classDefs appends one classDef per class name to the source, coloured from
// the current theme's values of the role variables. A page's roles map can
// add names or move one to another role where its own legend needs it.
function classDefs(roles) {
  return Object.entries({ ...ROLES, ...roles }).map(([name, role]) => {
    const weight = BOLD.has(role) ? ",font-weight:600" : "";
    return `classDef ${name} fill:${palette(`${role}-fill`)},stroke:${palette(`${role}-stroke`)},` +
      `color:${palette(`${role}-text`)},stroke-width:1.5px${weight}`;
  }).join("\n");
}

// config is the mermaid configuration for the current theme. Text takes the
// page's own font so labels match the prose around them.
function config() {
  return {
    startOnLoad: false,
    theme: "base",
    layout: "elk",
    // Edges into the same node share one trunk rather than a lane each, which
    // keeps a node with many inbound edges from widening the whole diagram.
    elk: { nodePlacementStrategy: "BRANDES_KOEPF", mergeEdges: true },
    themeVariables: {
      darkMode: isDark(),
      background: "transparent",
      fontFamily: getComputedStyle(document.body).fontFamily,
      fontSize: "14px",
      primaryColor: palette("process-fill"),
      primaryTextColor: palette("process-text"),
      primaryBorderColor: palette("process-stroke"),
      lineColor: palette("line"),
      textColor: palette("text"),
      edgeLabelBackground: palette("label-fill"),
    },
    // Mermaid sizes each label outside the page, and the page's prose styles
    // would then give the label's paragraph a margin and a taller line once it
    // is drawn, clipping the last line. This CSS is embedded in the svg under
    // its own id, so it outranks the page and applies to both.
    themeCSS: ".label p, .nodeLabel p, .edgeLabel p { margin: 0; line-height: 1.4; }",
    // Fit to the column, with spacing tight enough that the fit does not
    // shrink the text.
    flowchart: { curve: "basis", padding: 10, nodeSpacing: 24, rankSpacing: 36, useMaxWidth: true },
  };
}

// -------------------------------------------------------------------------
// TOOLTIP
// -------------------------------------------------------------------------

const tooltip = document.getElementById("dg-tooltip");
let mouseX = 0, mouseY = 0;
let pinned = false, hideTimer = null, hoveringTooltip = false, hoveringNode = false;

tooltip.addEventListener("mouseenter", () => { hoveringTooltip = true; clearTimeout(hideTimer); });
tooltip.addEventListener("mouseleave", () => { hoveringTooltip = false; scheduleHide(); });
document.addEventListener("mousemove", (e) => {
  mouseX = e.clientX; mouseY = e.clientY;
  if (tooltip.style.display === "block" && !pinned) positionTooltip();
});

// scheduleHide clears the tooltip shortly after the pointer leaves both the
// node and the tooltip, so it can move from one to the other.
function scheduleHide() {
  hideTimer = setTimeout(() => { if (!hoveringNode && !hoveringTooltip) clearInfo(); }, 100);
}

// positionTooltip keeps the panel beside the cursor and inside the viewport,
// preferring below and flipping above only when above has more room.
function positionTooltip() {
  const pad = 12;
  const w = tooltip.offsetWidth, h = tooltip.offsetHeight;
  const vw = window.innerWidth, vh = window.innerHeight;
  let x = mouseX + pad;
  if (x + w > vw - pad) x = mouseX - w - pad;
  x = Math.max(pad, Math.min(x, vw - w - pad));
  const below = vh - mouseY - pad * 2, above = mouseY - pad * 2;
  let y = (h <= below || below >= above) ? mouseY + pad : mouseY - h - pad;
  y = Math.max(pad, Math.min(y, vh - h - pad));
  tooltip.style.left = `${x}px`;
  tooltip.style.top = `${y}px`;
}

// showInfo fills the tooltip for one node, its badge in the colour of the role
// it names. A panel holding a link is pinned in place so the pointer can reach
// the link.
function showInfo(info, roles, id) {
  const entry = info[id];
  if (!entry) { tooltip.style.display = "none"; pinned = false; return; }
  const role = { ...ROLES, ...roles }[entry.badge] ?? entry.badge;
  tooltip.innerHTML = `<h3>${entry.title}</h3>` +
    `<span class="dg-badge" style="--badge: var(--dg-${role}-stroke)">${entry.badgeText}</span>${entry.body}`;
  pinned = false;
  tooltip.style.display = "block";
  positionTooltip();
  if (tooltip.querySelector("a")) pinned = true;
}

// clearInfo hides the tooltip and drops any path highlight.
function clearInfo() {
  tooltip.style.display = "none";
  pinned = false;
  const svg = document.querySelector("#dg-diagram svg");
  if (svg) {
    svg.classList.remove("highlighting");
    svg.querySelectorAll(".highlight").forEach((el) => el.classList.remove("highlight"));
  }
}

// -------------------------------------------------------------------------
// INTERACTIVITY
// -------------------------------------------------------------------------

// nodeId recovers the source id from a rendered node's element id, which
// mermaid writes as "flowchart-<id>-<n>".
const nodeId = (el) => el.id.replace(/^flowchart-/, "").replace(/-\d+$/, "");

// endpoints splits an edge id, which the elk layout writes as
// "L_<from>_<to>_<n>_<m>", into its two node ids. Node ids may themselves hold
// underscores, so the split is the one whose halves are both real nodes.
function endpoints(edgeId, ids) {
  const parts = edgeId.replace(/^L_/, "").replace(/_\d+_\d+$/, "").split("_");
  for (let i = 1; i < parts.length; i++) {
    const from = parts.slice(0, i).join("_"), to = parts.slice(i).join("_");
    if (ids.has(from) && ids.has(to)) return { from, to };
  }
  return null;
}

// wire gives every node its tooltip and highlights every path through it,
// upstream and downstream, while it is hovered. Invisible links only arrange
// nodes, so they are not paths.
function wire(svg, spec) {
  const nodes = [...svg.querySelectorAll(".node")];
  const ids = new Set(nodes.map(nodeId));
  // Labels carry no edge id; mermaid draws them in the same order as the
  // edges, so they pair by index before invisible links are dropped.
  const labels = [...svg.querySelectorAll("g.edgeLabel")];
  const edges = [...svg.querySelectorAll("path.flowchart-link")].map((path, i) => {
    if (path.classList.contains("edge-thickness-invisible")) return null;
    const ends = endpoints(path.id, ids);
    return ends && { ...ends, path, label: labels[i] };
  }).filter(Boolean);

  const reach = (start, from, to) => {
    const seen = new Set([start]), used = new Set(), queue = [start];
    while (queue.length) {
      const cur = queue.shift();
      edges.forEach((e, i) => {
        if (e[from] !== cur) return;
        used.add(i);
        if (!seen.has(e[to])) { seen.add(e[to]); queue.push(e[to]); }
      });
    }
    return { seen, used };
  };

  nodes.forEach((node) => {
    const id = nodeId(node);
    node.addEventListener("mouseenter", () => {
      hoveringNode = true;
      clearTimeout(hideTimer);
      const down = reach(id, "from", "to"), up = reach(id, "to", "from");
      svg.classList.add("highlighting");
      nodes.forEach((n) => n.classList.toggle("highlight", down.seen.has(nodeId(n)) || up.seen.has(nodeId(n))));
      edges.forEach((e, i) => {
        const on = down.used.has(i) || up.used.has(i);
        e.path.classList.toggle("highlight", on);
        e.label?.classList.toggle("highlight", on);
      });
      showInfo(spec.info, spec.roles, id);
    });
    node.addEventListener("mouseleave", () => { hoveringNode = false; scheduleHide(); });
  });
}

// -------------------------------------------------------------------------
// RENDER
// -------------------------------------------------------------------------

let renders = 0;

// render draws the page's diagram for the current theme and wires it.
async function render() {
  const spec = window.vagabondDiagram;
  const container = document.getElementById("dg-diagram");
  if (!spec || !container) return;
  mermaid.initialize(config());
  const { svg } = await mermaid.render(`dg-render-${renders++}`, `${spec.source}\n${classDefs(spec.roles)}`);
  container.innerHTML = svg;
  wire(container.querySelector("svg"), spec);
}

render();

// Hextra switches theme by toggling the "dark" class on the root element;
// other class changes there leave the diagram alone.
let rendered = isDark();
new MutationObserver(() => {
  if (isDark() === rendered) return;
  rendered = isDark();
  clearInfo();
  render();
}).observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
