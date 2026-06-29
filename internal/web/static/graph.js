(function () {
  const root = document.getElementById("graph");
  if (!root) return;

  const graph = JSON.parse(root.dataset.graph || "{\"nodes\":[],\"edges\":[]}");
  const baseTypes = ["repo", "mirror", "snapshot", "publish"];
  const extraTypes = Array.from(new Set((graph.nodes || []).map((node) => node.type)))
    .filter((type) => !baseTypes.includes(type))
    .sort();
  const types = extraTypes.concat(baseTypes);
  const colors = {
    repo: "#2d6cdf",
    mirror: "#2f855a",
    snapshot: "#9a5b13",
    publish: "#8a3ffc",
    unknown: "#64748b",
  };

  const colWidth = 300;
  const rowHeight = 88;
  const margin = 48;
  const nodeWidth = 220;
  const nodeHeight = 48;
  const columns = buildColumns();
  const maxRows = Math.max(1, ...types.map((type) => columns.get(type).length));
  const width = Math.max(1120, margin * 2 + colWidth * types.length);
  const height = Math.max(660, margin * 2 + maxRows * rowHeight);
  const positions = new Map();
  const nodeGroups = new Map();
  const edgeViews = [];

  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.setAttribute("width", width);
  svg.setAttribute("height", height);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", "aptly relation graph");

  const defs = el("defs");
  const marker = el("marker", {
    id: "arrow",
    viewBox: "0 0 10 10",
    refX: "9",
    refY: "5",
    markerWidth: "7",
    markerHeight: "7",
    orient: "auto-start-reverse",
  });
  marker.appendChild(el("path", { d: "M 0 0 L 10 5 L 0 10 z", fill: "#7b8794" }));
  defs.appendChild(marker);
  svg.appendChild(defs);

  const edgeLayer = el("g");
  const nodeLayer = el("g");
  svg.appendChild(edgeLayer);
  svg.appendChild(nodeLayer);

  types.forEach((type, col) => {
    const nodes = columns.get(type);
    const x = margin + col * colWidth;
    const heading = el("text", {
      x,
      y: 26,
      "font-size": "13",
      "font-weight": "700",
      fill: "#4b5b6a",
    });
    heading.textContent = type.toUpperCase();
    nodeLayer.appendChild(heading);

    nodes.forEach((node, row) => {
      positions.set(node.id, {
        x,
        y: margin + row * rowHeight,
        type,
        node,
      });
    });
  });

  for (const edge of graph.edges || []) {
    const path = el("path", {
      fill: "none",
      stroke: "#8d9aa7",
      "stroke-width": "1.6",
      "marker-end": "url(#arrow)",
    });
    edgeLayer.appendChild(path);

    let labelBg = null;
    let label = null;
    if (edge.label) {
      labelBg = el("rect", {
        rx: "4",
        fill: "#ffffff",
        stroke: "#d9e0e6",
      });
      label = el("text", {
        "font-size": "11",
        fill: "#4b5b6a",
      });
      label.textContent = edge.label;
      edgeLayer.appendChild(labelBg);
      edgeLayer.appendChild(label);
    }
    edgeViews.push({ edge, path, label, labelBg });
  }

  for (const pos of positions.values()) {
    const group = el("g", {
      transform: `translate(${pos.x}, ${pos.y})`,
      "data-node-id": pos.node.id,
      tabindex: "0",
    });
    group.classList.add("graph-node");
    group.appendChild(el("rect", {
      width: nodeWidth,
      height: nodeHeight,
      rx: "7",
      fill: "#ffffff",
      stroke: colors[pos.type] || "#64748b",
      "stroke-width": "2",
    }));
    group.appendChild(el("rect", {
      width: "7",
      height: nodeHeight,
      rx: "3",
      fill: colors[pos.type] || "#64748b",
    }));

    const label = el("text", {
      x: "18",
      y: "21",
      "font-size": "13",
      "font-weight": "700",
      fill: "#17202a",
    });
    label.textContent = truncate(pos.node.label, 27);
    group.appendChild(label);

    const id = el("text", {
      x: "18",
      y: "38",
      "font-size": "10",
      fill: "#6b7785",
    });
    id.textContent = truncate(pos.node.id, 36);
    group.appendChild(id);
    nodeLayer.appendChild(group);
    nodeGroups.set(pos.node.id, group);

    group.addEventListener("pointerdown", (event) => startDrag(event, pos.node.id));
  }

  if (!graph.nodes || graph.nodes.length === 0) {
    const empty = el("text", {
      x: "48",
      y: "64",
      "font-size": "16",
      fill: "#6b7785",
    });
    empty.textContent = "No graph nodes from aptly API";
    svg.appendChild(empty);
  }

  updateEdges();
  root.replaceChildren(svg);

  function buildColumns() {
    const byType = new Map(types.map((type) => [type, []]));
    const byID = new Map();
    for (const node of graph.nodes || []) {
      if (!byType.has(node.type)) byType.set(node.type, []);
      byType.get(node.type).push(node);
      byID.set(node.id, node);
    }

    const incoming = new Map();
    for (const edge of graph.edges || []) {
      if (!incoming.has(edge.to)) incoming.set(edge.to, []);
      incoming.get(edge.to).push(edge.from);
    }

    const rowHint = new Map();
    for (const type of types) {
      const nodes = byType.get(type);
      nodes.sort((a, b) => {
        const ah = averageIncomingRow(a.id, rowHint, incoming);
        const bh = averageIncomingRow(b.id, rowHint, incoming);
        if (ah !== bh) return ah - bh;
        return a.label.localeCompare(b.label);
      });
      nodes.forEach((node, index) => rowHint.set(node.id, index));
    }
    return byType;
  }

  function averageIncomingRow(id, rowHint, incoming) {
    const from = incoming.get(id) || [];
    const known = from.map((source) => rowHint.get(source)).filter((row) => row !== undefined);
    if (!known.length) return Number.MAX_SAFE_INTEGER;
    return known.reduce((sum, row) => sum + row, 0) / known.length;
  }

  function startDrag(event, id) {
    const pos = positions.get(id);
    if (!pos) return;

    event.preventDefault();
    const point = svgPoint(event);
    const offsetX = point.x - pos.x;
    const offsetY = point.y - pos.y;
    const group = nodeGroups.get(id);
    group.setPointerCapture(event.pointerId);

    const move = (moveEvent) => {
      const next = svgPoint(moveEvent);
      pos.x = clamp(next.x - offsetX, 8, width - nodeWidth - 8);
      pos.y = clamp(next.y - offsetY, 34, height - nodeHeight - 8);
      group.setAttribute("transform", `translate(${pos.x}, ${pos.y})`);
      updateEdges();
    };

    const stop = () => {
      group.releasePointerCapture(event.pointerId);
      group.removeEventListener("pointermove", move);
      group.removeEventListener("pointerup", stop);
      group.removeEventListener("pointercancel", stop);
    };

    group.addEventListener("pointermove", move);
    group.addEventListener("pointerup", stop);
    group.addEventListener("pointercancel", stop);
  }

  function updateEdges() {
    const laneUse = new Map();
    for (const view of edgeViews) {
      const from = positions.get(view.edge.from);
      const to = positions.get(view.edge.to);
      if (!from || !to) {
        setVisible(view, false);
        continue;
      }

      const x1 = from.x + nodeWidth;
      const y1 = from.y + nodeHeight / 2;
      const x2 = to.x;
      const y2 = to.y + nodeHeight / 2;
      const laneKey = `${Math.round((x1 + x2) / 2 / 20)}:${Math.round((y1 + y2) / 2 / 20)}`;
      const lane = laneUse.get(laneKey) || 0;
      laneUse.set(laneKey, lane + 1);

      const spread = (lane % 2 === 0 ? 1 : -1) * Math.ceil(lane / 2) * 18;
      const dx = Math.max(70, Math.abs(x2 - x1) * 0.5);
      const c1x = x1 + dx;
      const c1y = y1 + spread;
      const c2x = x2 - dx;
      const c2y = y2 + spread;
      view.path.setAttribute("d", `M ${x1} ${y1} C ${c1x} ${c1y}, ${c2x} ${c2y}, ${x2 - 10} ${y2}`);
      setVisible(view, true);

      if (view.label) {
        const lx = (x1 + x2) / 2 + 5;
        const ly = (y1 + y2) / 2 + spread - 4;
        view.label.setAttribute("x", lx);
        view.label.setAttribute("y", ly);
        const w = Math.max(22, view.edge.label.length * 6 + 8);
        view.labelBg.setAttribute("x", lx - 4);
        view.labelBg.setAttribute("y", ly - 12);
        view.labelBg.setAttribute("width", w);
        view.labelBg.setAttribute("height", 16);
      }
    }
  }

  function setVisible(view, visible) {
    const value = visible ? "visible" : "hidden";
    view.path.setAttribute("visibility", value);
    if (view.label) view.label.setAttribute("visibility", value);
    if (view.labelBg) view.labelBg.setAttribute("visibility", value);
  }

  function svgPoint(event) {
    const point = svg.createSVGPoint();
    point.x = event.clientX;
    point.y = event.clientY;
    return point.matrixTransform(svg.getScreenCTM().inverse());
  }

  function el(name, attrs) {
    const node = document.createElementNS("http://www.w3.org/2000/svg", name);
    for (const [key, value] of Object.entries(attrs || {})) {
      node.setAttribute(key, value);
    }
    return node;
  }

  function truncate(value, max) {
    value = String(value || "");
    return value.length > max ? value.slice(0, max - 1) + "..." : value;
  }

  function clamp(value, min, max) {
    return Math.min(max, Math.max(min, value));
  }
})();
