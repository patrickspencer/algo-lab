/* Frontend: a browse view (filters, problem table, preview), a solve view
   (statement, highlighted editor, attempts / hints / review / output) and a
   scratchpad (snippets, editor, program output), mirroring the TUI's layout
   and key bindings. */
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const LH = 20; // must match --lh in style.css
  const PRISM_LANG = {
    golang: "go", python: "python", python3: "python", pythondata: "python", pandas: "python",
    cpp: "cpp", c: "c", csharp: "csharp", java: "java", kotlin: "kotlin", swift: "swift", rust: "rust",
    ruby: "ruby", php: "php", scala: "scala", dart: "dart", racket: "racket", erlang: "erlang",
    elixir: "elixir", javascript: "javascript", typescript: "typescript", mysql: "sql", mssql: "sql",
    oraclesql: "sql", postgresql: "sql", bash: "bash",
  };
  const LEVEL_NAMES = { 1: "nudge", 2: "approach", 3: "walkthrough" };
  const TEMPLATES = {
    python3: '# scratchpad · ctrl+enter runs this with python3\n\nprint("hello")\n',
    golang: 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hello")\n}\n',
    javascript: '// scratchpad · ctrl+enter runs this with node\n\nconsole.log("hello");\n',
    typescript: '// scratchpad · ctrl+enter runs this with node\n\nconsole.log("hello");\n',
    ruby: '# scratchpad · ctrl+enter runs this with ruby\n\nputs "hello"\n',
    bash: '# scratchpad · ctrl+enter runs this with bash\n\necho hello\n',
    c: '#include <stdio.h>\n\nint main(void) {\n    printf("hello\\n");\n    return 0;\n}\n',
    cpp: '#include <iostream>\n\nint main() {\n    std::cout << "hello\\n";\n    return 0;\n}\n',
    rust: 'fn main() {\n    println!("hello");\n}\n',
    java: 'public class Main {\n    public static void main(String[] args) {\n        System.out.println("hello");\n    }\n}\n',
    swift: 'print("hello")\n',
    php: '<?php\necho "hello\\n";\n',
  };

  const state = {
    view: "browse",
    problems: [], attemptCounts: {}, filtered: [], cursor: 0,
    difficulty: "", topic: "", search: "",
    sideItems: [], sideCursor: 0,
    focus: "list",
    previewSlug: "", details: {}, previewTimer: null,
    status: {}, languages: [], runTimeout: 10,
    solve: null, scratch: null,
    msg: "", msgTimer: null,
    me: null, online: [], invites: [], duo: null, events: null,
    account: null, cache: null,
    lists: [], listFilter: "", listSet: null, listOrder: null,
  };

  // ------------------------------------------------------------------ helpers
  async function api(method, path, body) {
    const res = await fetch(path, {
      method, headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (res.status === 401) { if ($("login").hidden) showLogin(); throw new Error(data.error || "sign in first"); }
    if (!res.ok) throw new Error(data.error || res.statusText);
    return data;
  }
  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  const p2 = (n) => String(n).padStart(2, "0");
  function fmtTime(iso) {
    const d = new Date(iso);
    return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
  }
  function clock(iso) {
    const d = iso ? new Date(iso) : new Date();
    return `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
  }
  function setMsg(text, isErr) {
    state.msg = text; state.msgErr = !!isErr;
    clearTimeout(state.msgTimer);
    state.msgTimer = setTimeout(() => { state.msg = ""; renderFooter(); }, 5000);
    renderFooter();
  }
  function renderMarkdown(text) {
    if (window.marked) {
      try { return marked.parse(text, { breaks: true }); } catch (_) { /* fall through */ }
    }
    return `<pre style="white-space:pre-wrap">${esc(text)}</pre>`;
  }
  function firstCodeLine(code) {
    for (const l of code.split("\n")) {
      const t = l.trim().replace(/^[#/ ]+/, "").trim();
      if (!t || t === "<?php" || t.startsWith("class ")) continue;
      return t;
    }
    return "(empty)";
  }
  function isTyping(e) {
    const tag = (e.target.tagName || "").toLowerCase();
    return tag === "input" || tag === "select" || tag === "textarea";
  }

  // ------------------------------------------------------------------ focus
  function setFocus(name) {
    state.focus = name;
    document.querySelectorAll(".panel[data-focus]").forEach((p) => p.classList.toggle("focused", p.dataset.focus === name));
    if (name === "editor" && state.solve) state.solve.ed.focus();
    else if (name === "seditor" && state.scratch) state.scratch.ed.focus();
    else if (document.activeElement && document.activeElement !== document.body) document.activeElement.blur();
    renderFooter();
  }
  document.querySelectorAll(".panel[data-focus]").forEach((p) => {
    p.addEventListener("mousedown", () => { if (state.focus !== p.dataset.focus) setFocus(p.dataset.focus); });
  });

  // ================================================================== editor component
  // A textarea for editing layered over a Prism-highlighted <pre>, with a
  // line-number gutter, a cursor line and optional per-line tints.
  function makeEditor(container, opts) {
    container.innerHTML =
      '<div class="editor-scroll"><div class="gutter"></div><div class="code-area"><div class="line-bgs"></div>' +
      '<pre class="highlight" aria-hidden="true"><code></code></pre>' +
      '<textarea spellcheck="false" autocapitalize="off" autocorrect="off" wrap="off"></textarea></div></div>';
    const scroll = container.querySelector(".editor-scroll");
    const gutter = container.querySelector(".gutter");
    const bgs = container.querySelector(".line-bgs");
    const hl = container.querySelector("code");
    const ta = container.querySelector("textarea");
    ta.placeholder = opts.placeholder || "";
    let lang = "python3";
    let tints = [];

    function refresh() {
      const code = ta.value;
      hl.className = "language-" + (PRISM_LANG[lang] || "clike");
      hl.textContent = code + "\n";
      if (window.Prism) { try { Prism.highlightElement(hl); } catch (_) { /* plain text */ } }
      const lines = code.split("\n").length;
      let g = "";
      for (let i = 1; i <= lines; i++) g += `<div data-l="${i}">${i}</div>`;
      gutter.innerHTML = g;
      ta.style.height = (lines + 1) * LH + "px";
      bgs.style.height = (lines + 1) * LH + "px";
      drawTints();
      updateCursor();
    }
    function cursorLine() { return ta.value.slice(0, ta.selectionStart).split("\n").length; }
    function updateCursor() {
      const line = cursorLine();
      const col = ta.selectionStart - ta.value.lastIndexOf("\n", ta.selectionStart - 1);
      gutter.querySelectorAll(".cursor").forEach((d) => d.classList.remove("cursor"));
      const g = gutter.querySelector(`[data-l="${line}"]`);
      if (g) g.classList.add("cursor");
      let cur = bgs.querySelector(".cursor");
      if (!cur) { cur = document.createElement("div"); cur.className = "cursor"; bgs.prepend(cur); }
      cur.style.top = (line - 1) * LH + "px";
      cur.hidden = document.activeElement !== ta;
      const top = (line - 1) * LH;
      if (top < scroll.scrollTop) scroll.scrollTop = top;
      else if (top + LH > scroll.scrollTop + scroll.clientHeight) scroll.scrollTop = top + LH - scroll.clientHeight;
      if (opts.onCursor) opts.onCursor(line, col);
    }
    function drawTints() {
      bgs.querySelectorAll(".tint").forEach((d) => d.remove());
      gutter.querySelectorAll(".tint").forEach((d) => d.classList.remove("tint"));
      for (const t of tints) {
        for (let l = t.start; l <= t.end; l++) {
          const d = document.createElement("div");
          d.className = "tint " + t.cls;
          d.style.top = (l - 1) * LH + "px";
          if (t.cls === "current") bgs.append(d); else bgs.prepend(d);
          const g = gutter.querySelector(`[data-l="${l}"]`);
          if (g) g.classList.add("tint");
        }
      }
    }
    ta.addEventListener("input", () => { refresh(); if (opts.onInput) opts.onInput(); });
    ["keyup", "click", "select"].forEach((ev) => ta.addEventListener(ev, updateCursor));
    document.addEventListener("selectionchange", () => { if (document.activeElement === ta) updateCursor(); });
    ta.addEventListener("focus", () => { if (opts.onFocus) opts.onFocus(); updateCursor(); });
    ta.addEventListener("blur", updateCursor);
    ta.addEventListener("keydown", (e) => {
      const mod = e.ctrlKey || e.metaKey;
      if (e.key === "Tab" && !mod) {
        e.preventDefault();
        ta.setRangeText("    ", ta.selectionStart, ta.selectionEnd, "end");
        ta.dispatchEvent(new Event("input"));
      } else if (e.key === "Enter" && !mod) {
        e.preventDefault();
        const before = ta.value.slice(0, ta.selectionStart);
        const indent = (before.slice(before.lastIndexOf("\n") + 1).match(/^[ \t]*/) || [""])[0];
        ta.setRangeText("\n" + indent, ta.selectionStart, ta.selectionEnd, "end");
        ta.dispatchEvent(new Event("input"));
      } else if (e.key === "Escape") {
        e.preventDefault();
        if (opts.onEscape) opts.onEscape();
      }
    });

    return {
      ta,
      value: () => ta.value,
      set(code, resetCursor) { ta.value = code; if (resetCursor) ta.setSelectionRange(0, 0); refresh(); },
      setLang(l) { lang = l; refresh(); },
      refresh, updateCursor,
      setTints(list) { tints = list || []; drawTints(); },
      focus() { ta.focus({ preventScroll: true }); updateCursor(); },
      gotoLine(n) {
        const lines = ta.value.split("\n");
        let pos = 0;
        for (let i = 0; i < Math.min(n - 1, lines.length - 1); i++) pos += lines[i].length + 1;
        ta.setSelectionRange(pos, pos);
        updateCursor();
      },
    };
  }

  // ------------------------------------------------------------------ run output rendering
  function renderRunResult(el, infoEl, r) {
    if (r.running) {
      el.innerHTML = `<div class="running">Running ${esc(r.lang)}… (killed after ${state.runTimeout}s)</div>`;
      infoEl.innerHTML = "<span>running</span>";
      return;
    }
    if (r.error) {
      el.innerHTML = `<div class="err">Could not run</div><div>${esc(r.error)}</div>`;
      infoEl.innerHTML = "";
      return;
    }
    const res = r.result;
    if (!res) { el.innerHTML = '<span class="dim">ctrl+enter runs the code; stdout and stderr appear here.</span>'; infoEl.innerHTML = ""; return; }
    let html = "";
    if (res.step === "compile") html += '<div class="compile">compile failed</div>';
    if (res.stdout) html += `<span class="stdout">${esc(res.stdout)}</span>`;
    if (res.stderr) html += `<span class="stderr">${esc(res.stderr)}</span>`;
    if (!res.stdout && !res.stderr) html += '<span class="dim">(no output)</span>';
    el.innerHTML = html;
    const status = res.timedOut ? "timed out" : `exit ${res.exitCode}`;
    infoEl.innerHTML = `<span class="${res.exitCode === 0 && !res.timedOut ? "" : "err"}">${status}</span><span>${res.millis}ms</span><span>${clock(r.at)}</span>`;
  }
  async function runCode(lang, code, stdin, r, el, infoEl) {
    if (r.running) { setMsg("Already running"); return; }
    const info = state.languages.find((l) => l.slug === lang || (lang === "python" && l.slug === "python3"));
    if (info && !info.available) { setMsg(`Cannot run ${lang} here: ${info.tool} is not installed`, true); return; }
    r.running = true; r.lang = lang; r.error = null;
    renderRunResult(el, infoEl, r);
    try {
      r.result = await api("POST", "/api/run", { lang, code, stdin: stdin || "" });
    } catch (err) {
      r.error = err.message; r.result = null;
    } finally {
      r.running = false; r.at = new Date().toISOString();
      renderRunResult(el, infoEl, r);
    }
  }

  // ================================================================== browse
  async function loadProblems() {
    const data = await api("GET", "/api/problems");
    state.problems = data.problems;
    state.attemptCounts = data.attemptCounts || {};
    buildSidebar();
    applyFilters();
    renderSidebar();
  }
  function buildSidebar() {
    const counts = {};
    const diff = { Easy: 0, Medium: 0, Hard: 0 };
    for (const p of state.problems) {
      diff[p.difficulty] = (diff[p.difficulty] || 0) + 1;
      for (const t of p.tags) counts[t] = (counts[t] || 0) + 1;
    }
    const topics = Object.entries(counts).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).slice(0, 60);
    const listItems = [{ kind: "list", value: "", label: "All problems", count: state.problems.length }]
      .concat(state.lists.map((l) => ({ kind: "list", value: l.slug, label: l.name, count: l.count || 0, source: l.source })));
    const solved = state.problems.filter((p) => p.status === "ac").length;
    const tried = state.problems.filter((p) => p.status === "notac").length;
    const statusItems = solved || tried ? [
      { kind: "status", value: "", label: "Any", count: state.problems.length },
      { kind: "status", value: "ac", label: "Solved", count: solved },
      { kind: "status", value: "notac", label: "Attempted", count: tried },
      { kind: "status", value: "todo", label: "Unsolved", count: state.problems.length - solved - tried },
    ] : [];
    state.sideItems = [
      ...listItems,
      ...statusItems,
      { kind: "diff", value: "", label: "All", count: state.problems.length },
      { kind: "diff", value: "Easy", label: "Easy", count: diff.Easy },
      { kind: "diff", value: "Medium", label: "Medium", count: diff.Medium },
      { kind: "diff", value: "Hard", label: "Hard", count: diff.Hard },
      { kind: "topic", value: "", label: "All topics", count: state.problems.length },
      ...topics.map(([t, c]) => ({ kind: "topic", value: t, label: t, count: c })),
    ];
  }
  async function loadLists() {
    try { state.lists = (await api("GET", "/api/lists")).lists || []; } catch (_) { return; }
    buildSidebar(); renderSidebar();
  }
  async function selectList(slug) {
    if (!slug) { state.listFilter = ""; state.listSet = null; state.listOrder = null; applyFilters(); renderSidebar(); return; }
    try {
      const d = await api("GET", "/api/lists/" + slug);
      state.listFilter = slug;
      state.listSet = new Set(d.slugs);
      state.listOrder = new Map(d.slugs.map((sg, i) => [sg, i]));
      const li = state.lists.find((l) => l.slug === slug); if (li) li.count = d.slugs.length;
      setMsg(`${d.name}: ${d.slugs.length} problems, in list order`);
    } catch (err) { setMsg("Error: " + err.message, true); return; }
    buildSidebar(); applyFilters(); renderSidebar();
  }
  function matches(p, term) {
    if (!term) return true;
    return p.title.toLowerCase().includes(term) || p.id.startsWith(term) ||
      p.difficulty.toLowerCase().includes(term) || p.tags.some((t) => t.toLowerCase().includes(term));
  }
  function applyFilters() {
    const term = state.search.trim().toLowerCase();
    const prev = state.filtered[state.cursor];
    state.filtered = [];
    state.problems.forEach((p, i) => {
      if (state.difficulty && p.difficulty !== state.difficulty) return;
      if (state.topic && !p.tags.includes(state.topic)) return;
      if (state.statusFilter === "todo" ? p.status : (state.statusFilter && p.status !== state.statusFilter)) return;
      if (state.listSet && !state.listSet.has(p.slug)) return;
      if (!matches(p, term)) return;
      state.filtered.push(i);
    });
    if (state.listOrder) state.filtered.sort((a, b) => (state.listOrder.get(state.problems[a].slug) ?? 0) - (state.listOrder.get(state.problems[b].slug) ?? 0));
    state.cursor = Math.max(0, state.filtered.indexOf(prev));
    renderList();
    schedulePreview();
  }
  function renderSidebar() {
    let html = "";
    let lastKind = "";
    state.sideItems.forEach((it, i) => {
      if (it.kind !== lastKind) {
        const label = { list: "Lists", status: "Status", diff: "Difficulty", topic: "Topics" }[it.kind];
        html += `<div class="section"${i ? ' style="margin-top:12px"' : ""}>${label}</div>`;
        lastKind = it.kind;
      }
      const active = it.kind === "diff" ? state.difficulty === it.value : it.kind === "status" ? state.statusFilter === it.value : it.kind === "list" ? state.listFilter === it.value : state.topic === it.value;
      const cls = ["item", i === state.sideCursor && state.focus === "filters" ? "cursor" : "", it.kind === "diff" && it.value ? it.value.toLowerCase() : ""].join(" ");
      html += `<div class="${cls}" data-i="${i}"><span class="mark">${active ? "●" : ""}</span><span class="label">${esc(it.label)}</span><span class="count">${it.count || ""}</span></div>`;
    });
    $("sidebar").innerHTML = html;
    $("filters-info").innerHTML = state.difficulty || state.topic || state.statusFilter || state.listFilter ? "<span>esc clear</span>" : "";
  }
  $("sidebar").addEventListener("click", (e) => {
    const item = e.target.closest(".item");
    if (!item) return;
    state.sideCursor = +item.dataset.i;
    toggleSideItem();
  });
  function toggleSideItem() {
    const it = state.sideItems[state.sideCursor];
    if (!it) return;
    if (it.kind === "list") { selectList(it.value); return; }
    if (it.kind === "diff") state.difficulty = it.value;
    else if (it.kind === "status") state.statusFilter = it.value;
    else state.topic = state.topic === it.value ? "" : it.value;
    applyFilters();
    renderSidebar();
  }
  function renderList() {
    const rows = $("rows");
    if (!state.filtered.length) {
      rows.innerHTML = `<div class="empty">${state.problems.length ? "No problems match." : "Loading…"}</div>`;
    } else {
      let html = "";
      state.filtered.forEach((idx, i) => {
        const p = state.problems[idx];
        const mark = p.status === "ac" ? ' <span class="solved" title="solved">✓</span>'
          : p.status === "notac" ? ' <span class="tried" title="attempted, not yet accepted">○</span>'
          : state.attemptCounts[p.slug] ? ' <span class="mark" title="has saved attempts">✎</span>' : "";
        html += `<div class="row${i === state.cursor ? " cursor" : ""}" data-i="${i}">` +
          `<span class="col-id">${p.id}${mark}</span>` +
          `<span class="col-title">${esc(p.title)}</span>` +
          `<span class="col-diff ${p.difficulty.toLowerCase()}">${p.difficulty}</span></div>`;
      });
      rows.innerHTML = html;
    }
    const lf = state.listFilter && state.lists.find((l) => l.slug === state.listFilter);
    $("list-title").textContent = state.problems.length
      ? (lf ? `${lf.name} (${state.filtered.length})` : `Problems (${state.filtered.length}/${state.problems.length})`)
      : "Problems";
    updateListCursor();
    renderMetadata();
  }
  function updateListCursor() {
    const rows = $("rows");
    rows.querySelectorAll(".row.cursor").forEach((r) => r.classList.remove("cursor"));
    const row = rows.querySelector(`.row[data-i="${state.cursor}"]`);
    if (row) { row.classList.add("cursor"); row.scrollIntoView({ block: "nearest" }); }
    $("list-info").innerHTML = state.filtered.length ? `<span>${state.cursor + 1}/${state.filtered.length}</span>` : "";
  }
  function moveList(delta) {
    if (!state.filtered.length) return;
    state.cursor = Math.max(0, Math.min(state.filtered.length - 1, state.cursor + delta));
    updateListCursor();
    renderMetadata();
    schedulePreview();
  }
  $("rows").addEventListener("click", (e) => {
    const row = e.target.closest(".row");
    if (!row) return;
    const i = +row.dataset.i;
    if (i === state.cursor) return openSolve(selected().slug);
    state.cursor = i;
    updateListCursor(); renderMetadata(); schedulePreview();
  });
  $("rows").addEventListener("dblclick", (e) => { if (e.target.closest(".row")) openSolve(selected().slug); });
  function selected() { return state.problems[state.filtered[state.cursor]]; }
  $("search").addEventListener("input", (e) => { state.search = e.target.value; applyFilters(); });
  $("search").addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.target.blur(); setFocus("list"); }
    if (e.key === "Escape") { e.target.value = ""; state.search = ""; applyFilters(); e.target.blur(); setFocus("list"); }
    e.stopPropagation();
  });

  function schedulePreview() {
    clearTimeout(state.previewTimer);
    const p = selected();
    if (!p) { state.previewSlug = ""; $("preview").innerHTML = '<span class="dim">Select a problem to preview it.</span>'; $("preview-title").textContent = "Preview"; return; }
    state.previewSlug = p.slug;
    if (state.details[p.slug]) return renderPreview(state.details[p.slug]);
    state.previewTimer = setTimeout(async () => {
      try {
        const d = await loadDetail(p.slug);
        if (state.previewSlug === p.slug) renderPreview(d);
      } catch (err) { $("preview").innerHTML = `<span class="err">Could not load problem</span><br>${esc(err.message)}`; }
    }, 150);
  }
  async function loadDetail(slug) {
    const d = await api("GET", `/api/problems/${slug}`);
    state.details[slug] = d;
    return d;
  }
  function statementHTML(d) {
    if (d.original) {
      let html = `<h1>${esc(d.id)}. ${esc(d.title)}</h1>` + renderMarkdown(d.statementMd || "");
      if (d.examples && d.examples.length) {
        html += "<h3>Examples</h3>";
        d.examples.forEach((c, i) => {
          const by = {}; (c.args || []).forEach((a) => (by[a.name] = a.value));
          html += `<pre><strong>Example ${i + 1}</strong>\nInput: ${esc(by.input || "")}\nOutput: ${esc(by.output || "")}</pre>`;
        });
      }
      if (d.constraints && d.constraints.length) {
        html += "<h3>Constraints</h3><ul>" + d.constraints.map((c) => `<li><code>${esc(c)}</code></li>`).join("") + "</ul>";
      }
      html += '<p class="dim" style="margin-top:1em">Original, public-domain statement.</p>';
      return html;
    }
    let html = d.content || "";
    if (d.hints && d.hints.length) {
      html += '<details class="hints"><summary>Hints from the problem setter (' + d.hints.length + ")</summary><ol>" +
        d.hints.map((h) => `<li>${h}</li>`).join("") + "</ol></details>";
    }
    return html;
  }
  function renderPreview(d) {
    $("preview").innerHTML = statementHTML(d);
    $("preview").scrollTop = 0;
    $("preview-title").textContent = `${d.id}. ${d.title}`;
    $("preview-info").innerHTML = d.original ? '<span>original</span>' : `<span>${d.fromCache ? "cache" : "network"}</span>`;
    renderMetadata();
  }
  function renderMetadata() {
    renderPreviewMeta(selected());
  }
  // renderPreviewMeta puts the selected problem's facts under its description.
  function renderPreviewMeta(p) {
    const strip = $("preview-meta");
    if (!p) { strip.hidden = true; return; }
    const bits = [`<span class="${p.difficulty.toLowerCase()}">${p.difficulty}</span>`];
    strip.innerHTML = `<div class="line">${bits.join("   ")}</div><div class="line"><span class="k" style="width:auto">topics&nbsp;</span>${esc(p.tags.join(", "))}</div>`;
    strip.hidden = false;
  }
  function cacheLineHTML() {
    const c = state.cache;
    const signedIn = state.account && state.account.signedIn;
    if (c && c.running) {
      const pct = c.total ? Math.round((c.done / c.total) * 100) : 0;
      return `<div class="line"><span class="k">Cache</span><span class="accent">caching ${c.premiumOnly ? "locked" : "all"} problems…</span> ${c.done}/${c.total} (${pct}%)${c.failed ? ` · ${c.failed} failed` : ""} <button data-cache="stop">stop</button></div>`;
    }
    let tail = "";
    if (c && c.finishedAt && (c.done || c.failed)) tail = ` <span class="dim">last run: ${c.done} cached${c.failed ? `, ${c.failed} failed` : ""}${c.stopped ? ", stopped" : ""}</span>`;
    if (c && c.lastError && c.stopped) tail = ` <span class="err">${esc(c.lastError)}</span>`;
    const btn = signedIn
      ? `<button data-cache="premium" title="Fetch and cache every locked (premium) problem, slowly, to avoid rate limiting">cache locked problems</button> <button data-cache="all" title="Cache every problem's description">all</button>`
      : `<span class="dim">sign in on the site (see Account) to cache locked problems</span>`;
    return `<div class="line"><span class="k">Cache</span>${btn}${tail}</div>`;
  }
  let cachePoll = null;
  async function loadCacheStatus() {
    try { state.cache = await api("GET", "/api/cache"); } catch (_) { return; }
    renderStatus();
    clearTimeout(cachePoll);
    if (state.cache && state.cache.running) cachePoll = setTimeout(loadCacheStatus, 1200);
    else { buildSidebar(); renderSidebar(); renderList(); } // solved counts may have grown
  }
  async function startCache(premiumOnly) {
    try {
      state.cache = await api("POST", "/api/cache/start", { premiumOnly });
      setMsg(state.cache.total ? `Caching ${state.cache.total} problems in the background…` : "Everything is already cached");
      loadCacheStatus();
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  async function stopCache() {
    try { state.cache = await api("POST", "/api/cache/stop"); renderStatus(); setMsg("Stopped caching"); }
    catch (err) { setMsg("Error: " + err.message, true); }
  }
  $("settings-status").addEventListener("click", (e) => {
    const b = e.target.closest("[data-cache]");
    if (!b) return;
    if (b.dataset.cache === "stop") stopCache();
    else startCache(b.dataset.cache === "premium");
  });

  async function loadStatus() {
    try { state.status = await api("GET", "/api/status"); } catch (err) { state.status = { error: err.message }; }
    renderStatus();
  }
  function renderStatus() {
    const s = state.status;
    const ai = s.ai ? "on" : `<span class="dim">not configured</span>`;
    const runnable = state.languages.filter((l) => l.available).map((l) => l.slug).join(", ");
    const statusEl = $("settings-status"); if (!statusEl) return;
    statusEl.innerHTML =
      `<div class="line"><span class="k">You</span>${state.me ? esc(state.me.name) : '<span class="dim">not signed in</span>'}</div>` +
      `<div class="line"><span class="k">Database</span>${esc(s.dbPath || "")}</div>` +
      `<div class="line"><span class="k">Cached</span>${s.problems ?? 0} problems · ${s.details ?? 0} descriptions</div>` +
      `<div class="line"><span class="k">Last sync</span>${s.lastSync ? fmtTime(s.lastSync) : "never"}</div>` +
      `<div class="line"><span class="k">Account</span>${state.account && state.account.signedIn ? esc(state.account.username) + (state.account.premium ? " · premium" : "") : `<span class="dim">not signed in (${esc((state.account && state.account.sessionFile) || "session file")})</span>`}</div>` +
      cacheLineHTML() +
      `<div class="line"><span class="k">AI</span>${ai}</div>` +
      `<div class="line"><span class="k">Runners</span>${runnable ? esc(runnable) : '<span class="dim">none found</span>'}</div>` +
      `<div class="line"><span class="k">State</span>${s.syncing ? '<span class="accent">syncing…</span>' : '<span class="dim">idle</span>'}</div>`;
  }
  async function resync() {
    setMsg("Syncing the problem list…");
    state.status.syncing = true; renderStatus();
    try {
      const r = await api("POST", "/api/sync");
      await loadProblems();
      setMsg(`Synced ${r.synced} problems`);
    } catch (err) { setMsg("Error: " + err.message, true); }
    loadStatus();
  }

  // ================================================================== solve
  const solveEd = makeEditor($("editor"), {
    placeholder: "Write your solution…",
    onInput() {
      const s = state.solve; if (!s) return;
      s.loadedAttempt = 0;
      scheduleDraft();
      if (s.review && s.tab === "review") renderReview();
      renderEditorInfo();
      sendDuoUpdate(false);
    },
    onCursor() { renderEditorInfo(); if (state.duo) sendDuoUpdate(false); },
    onFocus() { if (state.focus !== "editor") setFocus("editor"); },
    onEscape() { setFocus("statement"); },
  });

  function showView(name) {
    ["browse", "solve", "scratch"].forEach((v) => { $(v).hidden = v !== name; });
    state.view = name;
  }

  async function openSolve(slug) {
    if (!slug) return;
    showView("solve");
    state.solve = { slug, ed: solveEd, loading: true, tab: "hint", leftTab: "statement", hints: {}, noteCursor: 0, attemptCursor: 0, confirmDelete: null, run: {} };
    $("statement-title").textContent = "Problem";
    $("statement").innerHTML = '<span class="dim">Loading…</span>';
    renderFooter();
    try {
      const [d, settings] = await Promise.all([api("GET", `/api/problems/${slug}`), api("GET", "/api/settings")]);
      state.details[slug] = d;
      const s = state.solve;
      if (!s || s.slug !== slug) return;
      s.detail = d;
      s.attempts = d.attempts || [];
      s.drafts = d.drafts || {};
      for (const h of d.aiHints || []) s.hints[h.level] = h;
      s.review = d.review || null;
      s.progress = d.progress || "";
      s.langs = (d.snippets || []).map((sn) => sn.langSlug);
      s.lang = s.langs.includes(settings.lang) ? settings.lang : (s.langs[0] || settings.lang);
      s.loading = false;
      $("statement-title").textContent = `${d.id}. ${d.title}`;
      $("statement").innerHTML = statementHTML(d);
      $("statement-diff").className = d.difficulty.toLowerCase();
      $("statement-diff").textContent = d.difficulty;
      renderLangSelect();
      solveEd.setLang(s.lang);
      setCode(codeFor(s.lang), true);
      renderAttempts();
      renderExamples();
      renderProgress();
      showTab("hint");
      setBottomHidden(!!state.bottomHiddenPref);
      showLeftTab(state.duo && state.duo.slug === slug ? "duo" : "statement");
      setFocus("editor");
      sendPresence();
    } catch (err) {
      setMsg("Error: " + err.message, true);
      closeSolve();
    }
  }
  function closeSolve() {
    flushDraft();
    if (state.duo) leaveDuo();
    $("partner").hidden = true;
    state.solve = null;
    showView("browse");
    setFocus("list");
    sendPresence();
    api("GET", "/api/problems").then((data) => { state.attemptCounts = data.attemptCounts || {}; renderList(); }).catch(() => {});
  }
  function snippetFor(lang) {
    const sn = (state.solve.detail.snippets || []).find((x) => x.langSlug === lang);
    return sn ? sn.code : "";
  }
  function codeFor(lang) {
    const s = state.solve;
    return lang in s.drafts ? s.drafts[lang] : snippetFor(lang);
  }
  function renderLangSelect() {
    const s = state.solve;
    const names = {};
    for (const sn of s.detail.snippets || []) names[sn.langSlug] = sn.lang;
    $("lang").innerHTML = s.langs.map((l) => `<option value="${l}"${l === s.lang ? " selected" : ""}>${esc(names[l] || l)}</option>`).join("");
    $("editor-lang").textContent = s.lang;
  }
  $("lang").addEventListener("change", (e) => {
    const s = state.solve; if (!s) return;
    flushDraft();
    s.lang = e.target.value;
    s.loadedAttempt = 0;
    $("editor-lang").textContent = s.lang;
    solveEd.setLang(s.lang);
    setCode(codeFor(s.lang), true);
    api("PUT", "/api/settings", { lang: s.lang }).catch(() => {});
    setMsg("Language: " + s.lang);
    setFocus("editor");
    sendDuoUpdate(true);
  });

  function setCode(code, resetCursor) {
    solveEd.set(code, resetCursor);
    state.solve.lastSaved = code;
    renderEditorInfo();
  }
  function renderEditorInfo() {
    const s = state.solve; if (!s) return;
    const ta = solveEd.ta;
    const line = ta.value.slice(0, ta.selectionStart).split("\n").length;
    const col = ta.selectionStart - ta.value.lastIndexOf("\n", ta.selectionStart - 1);
    let stateText = "saved";
    if (ta.value !== s.lastSaved) stateText = "modified";
    else if (s.loadedAttempt) stateText = `attempt #${s.loadedAttempt}`;
    else if (s.savedAt) stateText = "draft " + clock(s.savedAt);
    $("editor-info").innerHTML = `<span>${line}:${col}</span><span>${stateText}</span>`;
  }
  let draftTimer = null;
  function scheduleDraft() {
    clearTimeout(draftTimer);
    draftTimer = setTimeout(flushDraft, 600);
  }
  function flushDraft() {
    clearTimeout(draftTimer);
    const s = state.solve; if (!s || s.loading) return;
    const code = solveEd.value();
    if (code === s.lastSaved) return;
    s.lastSaved = code;
    s.drafts[s.lang] = code;
    api("PUT", `/api/problems/${s.slug}/draft`, { lang: s.lang, code }).then((r) => {
      if (state.solve === s) { s.savedAt = r.savedAt; renderEditorInfo(); }
    }).catch((err) => setMsg("Draft not saved: " + err.message, true));
  }
  async function saveAttempt() {
    const s = state.solve; if (!s || s.loading) return;
    const code = solveEd.value();
    if (!code.trim()) return setMsg("Nothing to save");
    try {
      const r = await api("POST", `/api/problems/${s.slug}/attempts`, { lang: s.lang, code });
      s.attempts = r.attempts; s.loadedAttempt = r.id; s.lastSaved = code; s.savedAt = new Date().toISOString();
      s.drafts[s.lang] = code; s.attemptCursor = 0;
      if (!s.progress) { s.progress = "attempted"; const p = state.problems.find((x)=>x.slug===s.slug); if (p && !p.status) p.status = "notac"; renderProgress(); }
      renderAttempts(); renderEditorInfo();
      setMsg(`Saved attempt #${r.id}`);
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  function resetCode() {
    const s = state.solve; if (!s) return;
    setCode(snippetFor(s.lang), true);
    s.lastSaved = null;
    flushDraft();
    setMsg("Reset to starter code");
  }
  function renderProgress() {
    const s = state.solve; if (!s) return;
    const cur = s.progress || "";
    document.querySelectorAll("#progress-control button").forEach((b) => b.classList.toggle("on", b.dataset.progress === cur && cur !== ""));
  }
  async function setProgress(status) {
    const s = state.solve; if (!s) return;
    const next = s.progress === status ? "" : status; // clicking the active one clears
    try {
      await api("POST", `/api/problems/${s.slug}/progress`, { status: next });
      s.progress = next;
      const p = state.problems.find((x) => x.slug === s.slug);
      if (p) p.status = next === "solved" ? "ac" : next === "attempted" ? "notac" : "";
      renderProgress();
      setMsg(next ? `Marked ${next}` : "Progress cleared");
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  document.getElementById("progress-control").addEventListener("click", (e) => {
    const b = e.target.closest("[data-progress]"); if (b) setProgress(b.dataset.progress);
  });
  function runSolve() {
    const s = state.solve; if (!s || s.loading) return;
    showTab("output");
    flushDraft();
    runCode(s.lang, solveEd.value(), "", s.run, $("tab-output"), $("bottom-info"));
  }

  // examples (the problem's visible test cases)
  function renderExamples() {
    const s = state.solve; if (!s || !s.detail) return;
    const el = $("tab-examples");
    const cases = s.detail.examples || [];
    let html = "";
    if (s.detail.signature) html += `<div class="sig"><code>${esc(s.detail.signature)}</code></div>`;
    if (!cases.length) { el.innerHTML = html + '<div class="none">No visible example cases for this problem.</div>'; return; }
    cases.forEach((c, i) => {
      const raw = (c.args || []).map((a) => a.value).join("\n");
      html += `<div class="case"><div class="case-head">case ${i + 1}<button data-copy-case="${i}" title="Copy the raw input">copy</button></div>`;
      (c.args || []).forEach((a) => {
        html += `<div class="arg"><span class="k">${esc(a.name || "input")}</span><pre>${esc(a.value)}</pre></div>`;
      });
      html += "</div>";
    });
    el.innerHTML = html;
    el.dataset.count = cases.length;
    $("examples-tab").textContent = cases.length ? `Examples (${cases.length})` : "Examples";
  }
  $("tab-examples").addEventListener("click", (e) => {
    const b = e.target.closest("[data-copy-case]"); if (!b) return;
    const c = (state.solve.detail.examples || [])[+b.dataset.copyCase];
    if (!c) return;
    const raw = (c.args || []).map((a) => a.value).join("\n");
    navigator.clipboard?.writeText(raw).then(() => setMsg("Copied case input")).catch(() => setMsg("Copy failed", true));
  });

  // bottom panel
  function setBottomHidden(hidden) {
    const s = state.solve; if (!s) return;
    s.bottomHidden = hidden;
    $("solve").classList.toggle("no-bottom", hidden);
    if (hidden && state.focus === "bottom") setFocus("editor");
    renderFooter();
  }
  function showTab(name) {
    const s = state.solve; if (!s) return;
    s.tab = name;
    if (s.bottomHidden) setBottomHidden(false); // anything that wants the box brings it back
    document.querySelectorAll("#bottom-tabs .tab").forEach((t) => t.classList.toggle("active", t.dataset.tab === name));
    ["hint", "review", "output", "examples"].forEach((t) => { $("tab-" + t).hidden = t !== name; });
    if (name === "examples") renderExamples();
    if (name === "review") renderReview(); else updateTints();
    renderBottomInfo();
    renderFooter();
  }
  $("bottom-tabs").addEventListener("click", (e) => {
    if (e.target.closest("#bottom-collapse")) return setBottomHidden(true);
    const t = e.target.closest(".tab"); if (t) { showTab(t.dataset.tab); setFocus("bottom"); }
  });
  function renderBottomInfo() {
    const s = state.solve; const el = $("bottom-info");
    if (!s) return;
    if (s.tab === "hint") {
      const h = s.hints[s.hintLevel];
      el.innerHTML = (h && h.cached ? "<span>cached</span>" : "") + "<span>r again</span>";
    } else if (s.tab === "output") renderRunResult($("tab-output"), el, s.run);
    else {
      const r = s.review;
      el.innerHTML = r && r.review ? (r.review.notes.length ? `<span>note ${s.noteCursor + 1}/${r.review.notes.length}</span>` : "") + "<span>j/k notes</span><span>r again</span>" : "";
    }
  }
  function renderAttempts() {
    const s = state.solve;
    const el = $("tab-attempts");
    $("attempts-tab").textContent = s.attempts.length ? `Attempts (${s.attempts.length})` : "Attempts";
    if (!s.attempts.length) { el.innerHTML = '<div class="dim">No saved attempts yet. ctrl+s saves one.</div>'; renderBottomInfo(); return; }
    el.innerHTML = s.attempts.map((a, i) =>
      `<div class="attempt${i === s.attemptCursor && state.focus === "statement" && s.leftTab === "attempts" ? " cursor" : ""}${a.id === s.loadedAttempt ? " loaded" : ""}" data-i="${i}">` +
      `<span>#${a.id}</span><span>${fmtTime(a.createdAt)}</span><span>${esc(a.lang)}</span><span>${a.code.split("\n").length} lines</span>` +
      `<span class="summary">${esc(firstCodeLine(a.code))}</span>` +
      `<span class="del" data-del="${a.id}" title="delete">${s.confirmDelete === a.id ? '<span class="err">delete? click again</span>' : "✕"}</span></div>`).join("");
    renderBottomInfo();
  }
  $("tab-attempts").addEventListener("click", async (e) => {
    const s = state.solve; if (!s) return;
    const del = e.target.closest("[data-del]");
    if (del) {
      const id = +del.dataset.del;
      if (s.confirmDelete !== id) { s.confirmDelete = id; renderAttempts(); return; }
      s.confirmDelete = null;
      await deleteAttempt(id);
      return;
    }
    const row = e.target.closest(".attempt");
    if (!row) return;
    s.attemptCursor = +row.dataset.i;
    loadAttempt(s.attempts[s.attemptCursor]);
  });
  async function deleteAttempt(id) {
    const s = state.solve;
    try {
      await api("DELETE", `/api/attempts/${id}`);
      s.attempts = s.attempts.filter((a) => a.id !== id);
      s.attemptCursor = Math.min(s.attemptCursor, Math.max(0, s.attempts.length - 1));
      renderAttempts();
      setMsg(`Deleted attempt #${id}`);
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  function loadAttempt(a) {
    const s = state.solve; if (!a) return;
    flushDraft();
    if (a.lang !== s.lang) {
      s.lang = a.lang; renderLangSelect(); solveEd.setLang(a.lang);
      api("PUT", "/api/settings", { lang: a.lang }).catch(() => {});
    }
    setCode(a.code, true);
    s.loadedAttempt = a.id;
    s.lastSaved = null; flushDraft(); s.lastSaved = a.code;
    renderAttempts();
    renderEditorInfo();
    setMsg(`Loaded attempt #${a.id}`);
    setFocus("editor");
  }

  // thinking shows an animated, elapsed-time loading block inside el and
  // returns a stop function. Makes a slow CLI call obviously still working.
  function thinking(el, label) {
    const started = Date.now();
    el.innerHTML = `<div class="loading"><span class="spin">⋯</span> ${esc(label)} <span class="secs">0s</span><div class="dim think-sub">This can take a few seconds — you can keep editing meanwhile.</div></div>`;
    const t = setInterval(() => {
      const sp = el.querySelector(".secs");
      if (sp) sp.textContent = Math.round((Date.now() - started) / 1000) + "s";
      else clearInterval(t);
    }, 1000);
    return () => clearInterval(t);
  }

  async function requestHint(level, force) {
    const s = state.solve; if (!s || s.loading) return;
    s.hintLevel = level;
    showTab("hint");
    const el = $("tab-hint");
    const levels = () => `<div class="hint-levels">` + [1, 2, 3].map((l) =>
      `<button class="${l === level ? "primary" : ""}" data-level="${l}">hint ${l} · ${LEVEL_NAMES[l]}</button>`).join("") +
      `<button data-level="${level}" data-force="1" title="regenerate">↻</button></div>`;
    if (!force && s.hints[level]) {
      el.innerHTML = levels() + `<div class="hint-text">${renderMarkdown(s.hints[level].text)}</div>`;
      renderBottomInfo();
      return;
    }
    if (s.hintBusy) return setMsg("A hint is already generating…");
    s.hintBusy = true;
    el.innerHTML = levels() + `<div class="think"></div>`;
    const stop = thinking(el.querySelector(".think"), `Generating a level ${level} hint (${LEVEL_NAMES[level]})…`);
    setMsg(`Generating a level ${level} hint…`);
    try {
      const h = await api("POST", `/api/problems/${s.slug}/hint`, { level, lang: s.lang, code: solveEd.value(), force: !!force });
      if (state.solve !== s) return;
      s.hints[level] = h;
      if (s.hintLevel === level) el.innerHTML = levels() + `<div class="hint-text">${renderMarkdown(h.text)}</div>`;
      setMsg("Hint ready");
    } catch (err) {
      if (state.solve === s) el.innerHTML = levels() + `<div class="err">Hint request failed</div><div>${esc(err.message)}</div>`;
      setMsg("Hint failed: " + err.message, true);
    } finally { stop(); s.hintBusy = false; renderBottomInfo(); }
  }
  $("tab-hint").addEventListener("click", (e) => {
    const b = e.target.closest("[data-level]");
    if (b) requestHint(+b.dataset.level, !!b.dataset.force);
  });

  async function requestReview(force) {
    const s = state.solve; if (!s || s.loading) return;
    const code = solveEd.value();
    if (!code.trim()) return setMsg("Nothing to review yet");
    showTab("review");
    if (!force && s.review && s.review.code === code) { s.noteCursor = 0; renderReview(); return; }
    if (s.reviewBusy) return setMsg("A review is already generating…");
    s.reviewBusy = true;
    flushDraft();
    const stop = thinking($("tab-review"), `Reviewing your ${esc(s.lang)} code…`);
    setMsg(`Reviewing your ${s.lang} code…`);
    try {
      const r = await api("POST", `/api/problems/${s.slug}/review`, { lang: s.lang, code, force: !!force });
      if (state.solve !== s) return;
      s.review = r; s.noteCursor = 0;
      renderReview();
      if (r.review.notes.length && r.code === solveEd.value()) solveEd.gotoLine(r.review.notes[0].start);
      setMsg("Review ready");
    } catch (err) {
      if (state.solve === s) $("tab-review").innerHTML = `<div class="err">Review request failed</div><div style="white-space:pre-wrap">${esc(err.message)}</div>`;
      setMsg("Review failed: " + err.message, true);
    } finally { stop(); s.reviewBusy = false; renderBottomInfo(); }
  }
  function renderReview() {
    const s = state.solve; if (!s) return;
    const el = $("tab-review");
    const r = s.review;
    if (!r || !r.review) { el.innerHTML = '<div class="dim">No review yet. Press R (or ctrl+enter in the editor) to ask for one.</div>'; updateTints(); renderBottomInfo(); return; }
    const stale = r.code !== solveEd.value();
    const rv = r.review;
    let html = `<div><span class="verdict ${esc(rv.verdict)}">${esc(rv.verdict)}</span>${stale ? ' <span class="stale">(code changed since this review)</span>' : ""}` +
      ` <button data-rereview="1" style="float:right">↻ review again</button></div><div class="review-summary" style="margin:2px 0 8px">${esc(rv.summary)}</div>`;
    if (!rv.notes.length) html += '<div class="dim">No line notes.</div>';
    rv.notes.forEach((n, i) => {
      const where = n.end > n.start ? `L${n.start}-${n.end}` : `L${n.start}`;
      html += `<div class="note${i === s.noteCursor ? " cursor" : ""}" data-i="${i}"><span class="mark">${i === s.noteCursor ? "▶" : "&nbsp;"}</span> <span class="sev ${esc(n.severity)}">${esc(n.severity)}</span><span class="where">${where}</span><div class="text">${esc(n.text)}</div></div>`;
    });
    el.innerHTML = html;
    updateTints();
    renderBottomInfo();
  }
  $("tab-review").addEventListener("click", (e) => {
    const s = state.solve; if (!s) return;
    if (e.target.closest("[data-rereview]")) return requestReview(true);
    const note = e.target.closest(".note");
    if (!note) return;
    s.noteCursor = +note.dataset.i;
    renderReview();
    if (s.review.code === solveEd.value()) solveEd.gotoLine(s.review.review.notes[s.noteCursor].start);
  });
  function updateTints() {
    const s = state.solve;
    if (!s || s.tab !== "review" || !s.review || !s.review.review || s.review.code !== solveEd.value()) { solveEd.setTints([]); return; }
    solveEd.setTints(s.review.review.notes.map((n, i) => ({ start: n.start, end: n.end, cls: i === s.noteCursor ? "current" : n.severity })));
  }
  function moveNote(delta) {
    const s = state.solve;
    if (!s.review || !s.review.review || !s.review.review.notes.length) return;
    const n = s.review.review.notes.length;
    s.noteCursor = Math.max(0, Math.min(n - 1, s.noteCursor + delta));
    renderReview();
    if (s.review.code === solveEd.value()) solveEd.gotoLine(s.review.review.notes[s.noteCursor].start);
  }
  $("btn-save").addEventListener("click", saveAttempt);
  $("btn-reset").addEventListener("click", resetCode);
  $("btn-run").addEventListener("click", runSolve);
  $("btn-review").addEventListener("click", () => requestReview(false));
  [1, 2, 3].forEach((l) => $("btn-hint" + l).addEventListener("click", () => requestHint(l, false)));

  // ================================================================== scratch
  const scratchEd = makeEditor($("scratch-editor"), {
    placeholder: "Type some code and press ctrl+enter to run it.",
    onInput() { const s = state.scratch; if (!s) return; scheduleScratchSave(); renderScratchInfo(); },
    onCursor() { renderScratchInfo(); },
    onFocus() { if (state.focus !== "seditor") setFocus("seditor"); },
    onEscape() { setFocus("scratches"); },
  });

  async function openScratchView() {
    showView("scratch");
    state.scratch = { ed: scratchEd, list: [], cursor: 0, id: 0, lang: "python3", run: {}, confirmDelete: null };
    renderScratchLangSelect();
    setFocus("seditor");
    await loadScratches(0);
  }
  function closeScratchView() {
    flushScratch();
    state.scratch = null;
    showView("browse");
    setFocus("list");
  }
  async function loadScratches(openID) {
    const s = state.scratch;
    try {
      const data = await api("GET", "/api/scratches");
      if (state.scratch !== s) return;
      s.list = data.scratches || [];
      if (!s.list.length) return newScratch(defaultRunLang());
      let target = openID || s.id;
      s.cursor = Math.max(0, s.list.findIndex((sc) => sc.id === target));
      if (!target || s.list[s.cursor].id !== s.id) openScratch(s.list[s.cursor]);
      renderScratchList();
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  function defaultRunLang() {
    const avail = state.languages.filter((l) => l.available).map((l) => l.slug);
    return avail.includes("python3") ? "python3" : (avail[0] || "python3");
  }
  async function newScratch(lang) {
    const s = state.scratch; if (!s) return;
    flushScratch();
    try {
      const r = await api("POST", "/api/scratches", { lang, code: TEMPLATES[lang] || "" });
      await loadScratches(r.id);
      setFocus("seditor");
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  function openScratch(sc) {
    const s = state.scratch;
    s.id = sc.id; s.lang = sc.lang; s.lastSaved = sc.code; s.run = {};
    $("scratch-lang").textContent = sc.lang;
    $("scratch-lang-select").value = sc.lang;
    scratchEd.setLang(sc.lang);
    scratchEd.set(sc.code, true);
    renderRunResult($("scratch-output"), $("output-info"), s.run);
    renderScratchInfo();
    renderScratchList();
  }
  function renderScratchList() {
    const s = state.scratch; if (!s) return;
    $("scratches-title").textContent = `Scratches (${s.list.length})`;
    $("scratch-list").innerHTML = s.list.map((sc, i) =>
      `<div class="scratch-item${i === s.cursor && state.focus === "scratches" ? " cursor" : ""}${sc.id === s.id ? " open" : ""}" data-i="${i}">` +
      `<span class="lang">${esc(sc.lang)}</span><span class="title">${esc(firstCodeLine(sc.id === s.id ? scratchEd.value() : sc.code))}</span></div>`).join("") ||
      '<div class="dim">No scratches yet.</div>';
  }
  $("scratch-list").addEventListener("click", (e) => {
    const s = state.scratch; if (!s) return;
    const item = e.target.closest(".scratch-item"); if (!item) return;
    const i = +item.dataset.i;
    if (i === s.cursor && s.list[i].id === s.id) return setFocus("seditor");
    s.cursor = i;
    flushScratch();
    openScratch(s.list[i]);
  });
  function renderScratchInfo() {
    const s = state.scratch; if (!s) return;
    const ta = scratchEd.ta;
    const line = ta.value.slice(0, ta.selectionStart).split("\n").length;
    const col = ta.selectionStart - ta.value.lastIndexOf("\n", ta.selectionStart - 1);
    let stateText = "saved";
    if (ta.value !== s.lastSaved) stateText = "modified";
    else if (s.savedAt) stateText = "saved " + clock(s.savedAt);
    $("scratch-editor-info").innerHTML = `<span>${line}:${col}</span><span>${stateText}</span>`;
  }
  let scratchTimer = null;
  function scheduleScratchSave() { clearTimeout(scratchTimer); scratchTimer = setTimeout(flushScratch, 600); }
  function flushScratch() {
    clearTimeout(scratchTimer);
    const s = state.scratch; if (!s || !s.id) return;
    const code = scratchEd.value();
    if (code === s.lastSaved) return;
    s.lastSaved = code;
    const sc = s.list.find((x) => x.id === s.id);
    if (sc) sc.code = code;
    api("PUT", `/api/scratches/${s.id}`, { lang: s.lang, code }).then((r) => {
      if (state.scratch === s) { s.savedAt = r.savedAt; renderScratchInfo(); renderScratchList(); }
    }).catch((err) => setMsg("Not saved: " + err.message, true));
  }
  function renderScratchLangSelect() {
    const avail = state.languages.filter((l) => l.available);
    $("scratch-lang-select").innerHTML = avail.map((l) => `<option value="${l.slug}">${esc(l.name)}</option>`).join("") ||
      '<option value="python3">python3 (not installed)</option>';
  }
  $("scratch-lang-select").addEventListener("change", (e) => {
    const s = state.scratch; if (!s) return;
    const lang = e.target.value;
    const untouched = !scratchEd.value().trim() || scratchEd.value() === TEMPLATES[s.lang];
    s.lang = lang;
    $("scratch-lang").textContent = lang;
    scratchEd.setLang(lang);
    if (untouched) scratchEd.set(TEMPLATES[lang] || "", true);
    s.lastSaved = null; flushScratch();
    setMsg("Language: " + lang);
    setFocus("seditor");
  });
  function setOutputHidden(hidden) {
    const s = state.scratch; if (!s) return;
    s.outputHidden = hidden;
    $("scratch").classList.toggle("no-bottom", hidden);
    if (hidden && state.focus === "output") setFocus("seditor");
    renderFooter();
  }
  $("output-collapse").addEventListener("click", () => setOutputHidden(true));
  function runScratch() {
    const s = state.scratch; if (!s) return;
    if (s.outputHidden) setOutputHidden(false);
    flushScratch();
    runCode(s.lang, scratchEd.value(), $("stdin").value, s.run, $("scratch-output"), $("output-info"));
  }
  async function deleteScratch(id) {
    const s = state.scratch; if (!s) return;
    try {
      await api("DELETE", `/api/scratches/${id}`);
      if (s.id === id) s.id = 0;
      s.confirmDelete = null;
      await loadScratches(0);
      setMsg(`Deleted scratch #${id}`);
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  function confirmDeleteScratch(id) {
    const s = state.scratch; if (!s || !id) return;
    if (s.confirmDelete === id) return deleteScratch(id);
    s.confirmDelete = id;
    setMsg(`Delete scratch #${id}? press x (or the delete button) again to confirm`);
  }
  $("btn-scratch-run").addEventListener("click", runScratch);
  $("btn-scratch-new").addEventListener("click", () => newScratch(state.scratch ? state.scratch.lang : defaultRunLang()));
  $("btn-scratch-delete").addEventListener("click", () => { if (state.scratch) confirmDeleteScratch(state.scratch.id); });
  $("stdin").addEventListener("keydown", (e) => { if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); runScratch(); } e.stopPropagation(); });

  // ================================================================== sign-in
  function showLogin(which) {
    $("login").hidden = false;
    showLoginTab(which || "signin");
  }
  function showLoginTab(which) {
    const signup = which === "signup";
    $("signin-form").hidden = signup;
    $("signup-form").hidden = !signup;
    setTimeout(() => $(signup ? "signup-name" : "signin-name").focus(), 30);
  }
  $("to-signup").addEventListener("click", (e) => { e.preventDefault(); $("signin-error").textContent = ""; showLoginTab("signup"); });
  $("to-signin").addEventListener("click", (e) => { e.preventDefault(); $("signup-error").textContent = ""; showLoginTab("signin"); });

  async function submitAuth(signup) {
    const nameEl = signup ? "signup-name" : "signin-name";
    const pwEl = signup ? "signup-password" : "signin-password";
    const errEl = signup ? "signup-error" : "signin-error";
    const name = $(nameEl).value.trim();
    if (!name) return;
    const body = { name, password: $(pwEl).value, signup };
    if (signup) body.code = $("signup-code").value.trim();
    try {
      state.me = await api("POST", "/api/login", body);
      $("login").hidden = true;
      $(errEl).textContent = "";
      $(pwEl).value = "";
      if (signup) $("signup-code").value = "";
      await boot();
    } catch (err) { $(errEl).textContent = err.message; }
  }
  $("signin-form").addEventListener("submit", (e) => { e.preventDefault(); submitAuth(false); });
  $("signup-form").addEventListener("submit", (e) => { e.preventDefault(); submitAuth(true); });
  ["signin-name", "signin-password", "signup-name", "signup-password", "signup-code"].forEach((id) =>
    $(id).addEventListener("keydown", (e) => e.stopPropagation()));

  $("pw-save").addEventListener("click", async () => {
    const cur = $("pw-current").value, nw = $("pw-new").value, confirm = $("pw-confirm").value;
    if (nw.length < 4) { $("pw-note").textContent = "at least 4 characters"; return; }
    if (nw !== confirm) { $("pw-note").textContent = "the two passwords do not match"; return; }
    try {
      await api("POST", "/api/password", { current: cur, new: nw });
      $("pw-current").value = ""; $("pw-new").value = ""; $("pw-confirm").value = "";
      $("pw-note").textContent = "password updated";
      if (state.me) state.me.hasPassword = true;
    } catch (err) { $("pw-note").textContent = err.message; }
  });

  // ================================================================== presence & duo
  function connectEvents() {
    if (state.events) state.events.close();
    const es = new EventSource("/api/events");
    state.events = es;
    es.addEventListener("presence", (e) => { state.online = JSON.parse(e.data).users || []; renderOnline(); });
    es.addEventListener("invite", (e) => {
      const inv = JSON.parse(e.data);
      state.invites = state.invites.filter((i) => i.id !== inv.id).concat(inv);
      renderOnline();
      setMsg(`${inv.from.name} invites you to duo on ${inv.title || inv.slug}`);
    });
    es.addEventListener("invite-declined", (e) => { const d = JSON.parse(e.data); setMsg(`${d.by.name} declined`); });
    es.addEventListener("duo-start", (e) => startDuo(JSON.parse(e.data)));
    es.addEventListener("duo-update", (e) => {
      const d = JSON.parse(e.data);
      if (state.duo && d.session === state.duo.session) applyPartner(d);
    });
    es.addEventListener("duo-end", (e) => {
      const d = JSON.parse(e.data);
      if (state.duo && d.session === state.duo.session) endDuo(d.by && d.by.id !== state.me.id ? `${d.by.name} left the duo` : "Left the duo");
    });
    es.onerror = () => { /* EventSource reconnects on its own */ };
  }
  function sendPresence() {
    const slug = state.view === "solve" && state.solve ? state.solve.slug : "";
    const lang = state.solve ? state.solve.lang : "";
    api("POST", "/api/presence", { slug, lang }).catch(() => {});
  }
  function inviteSlug() {
    if (state.view === "solve" && state.solve) return state.solve.slug;
    const p = selected();
    return p ? p.slug : "";
  }
  async function invite(userID) {
    const slug = inviteSlug();
    if (!slug) return setMsg("Select a problem first");
    try {
      const inv = await api("POST", "/api/duo/invite", { to: userID, slug });
      setMsg(`Invited ${inv.to.name} to ${inv.title || slug}; waiting for them to accept`);
    } catch (err) { setMsg("Error: " + err.message, true); }
  }
  async function respond(id, accept) {
    state.invites = state.invites.filter((i) => i.id !== id);
    renderOnline();
    try { await api("POST", "/api/duo/respond", { id, accept }); }
    catch (err) { setMsg("Error: " + err.message, true); }
  }
  function peopleHTML() {
    const me = state.me || {};
    let html = "";
    for (const inv of state.invites) {
      html += `<div class="person invite"><span class="who"><span class="name">${esc(inv.from.name)}</span> <span class="where">invites you: ${esc(inv.title || inv.slug)}</span></span>` +
        `<span class="actions"><button class="primary" data-accept="${inv.id}">accept</button><button data-decline="${inv.id}">decline</button></span></div>`;
    }
    const others = state.online.filter((u) => u.id !== me.id);
    html += `<div class="person me"><span class="who"><span class="dot">●</span> <span class="name">${esc(me.name || "")}</span> <span class="where">(you)</span></span></div>`;
    if (!others.length) html += '<div class="dim" style="padding:0 4px">Nobody else is connected. Share this server\'s address with a friend.</div>';
    for (const u of others) {
      const where = u.duo ? "in a duo" : (u.title ? "on " + esc(u.title) : "browsing");
      const busy = !!u.duo || !!(state.duo);
      html += `<div class="person"><span class="who"><span class="dot">●</span> <span class="name">${esc(u.name)}</span> <span class="where">${where}</span></span>` +
        `<span class="actions"><button data-invite="${u.id}" ${busy ? "disabled" : ""} title="Invite to duo on ${esc(inviteSlug() || "the selected problem")}">invite</button></span></div>`;
    }
    return html;
  }
  function renderTopbar() {
    $("topbar-user").textContent = state.me ? state.me.name : "not signed in";
    const me = state.me || {};
    let html = "";
    for (const inv of state.invites) {
      html += `<span class="inv">${esc(inv.from.name)} invites you to ${esc(inv.title || inv.slug)}<button class="primary" data-accept="${inv.id}">accept</button><button data-decline="${inv.id}">decline</button></span>`;
    }
    const others = state.online.filter((u) => u.id !== me.id);
    for (const u of others) {
      const where = u.duo ? "in a duo" : (u.title ? "on " + esc(u.title) : "browsing");
      const busy = !!u.duo || !!state.duo;
      html += `<span class="peer">● ${esc(u.name)} <span class="where">${where}</span>${busy ? "" : `<button data-invite="${u.id}" title="Invite to duo">invite</button>`}</span>`;
    }
    if (!others.length && !state.invites.length) html = '<span class="dim">nobody else online</span>';
    $("topbar-online").innerHTML = html;
  }
  $("topbar-online").addEventListener("click", onPeopleClick);
  function renderOnline() {
    renderTopbar();
    if (state.solve) {
      $("duo-people").innerHTML = state.duo ? "" : peopleHTML();
      $("duo-tab").innerHTML = "Duo" + (state.duo ? ' <span class="badge">●</span>' : (state.invites.length ? ' <span class="badge">!</span>' : ""));
    }
  }
  function onPeopleClick(e) {
    const inv = e.target.closest("[data-invite]");
    if (inv) return invite(+inv.dataset.invite);
    const acc = e.target.closest("[data-accept]");
    if (acc) return respond(+acc.dataset.accept, true);
    const dec = e.target.closest("[data-decline]");
    if (dec) return respond(+dec.dataset.decline, false);
  }
  $("duo-people").addEventListener("click", onPeopleClick);

  // Partner's live editor (read-only mirror).
  const partnerEd = makeEditor($("partner-editor"), { placeholder: "Waiting for your partner's code…" });
  partnerEd.ta.readOnly = true;
  partnerEd.ta.tabIndex = -1;

  function showLeftTab(name) {
    document.querySelectorAll("#left-tabs .tab").forEach((t) => t.classList.toggle("active", t.dataset.ltab === name));
    $("statement").hidden = name !== "statement";
    $("duo-pane").hidden = name !== "duo";
    $("attempts-pane").hidden = name !== "attempts";
    if (state.solve) state.solve.leftTab = name;
    if (name === "duo") renderOnline();
    if (name === "attempts") renderAttempts();
    renderFooter();
  }
  // toggleLeftTab shows a tab, or goes back to the problem if it is already showing.
  function toggleLeftTab(name) {
    const s = state.solve; if (!s) return;
    showLeftTab(s.leftTab === name ? "statement" : name);
  }
  $("left-tabs").addEventListener("click", (e) => {
    const t = e.target.closest("[data-ltab]");
    if (t) showLeftTab(t.dataset.ltab);
  });

  async function startDuo(d) {
    state.duo = { session: d.session, slug: d.slug, title: d.title, partner: d.partner, partnerCode: "", partnerCursor: 0, partnerLang: "", ended: false };
    state.invites = [];
    if (!(state.view === "solve" && state.solve && state.solve.slug === d.slug)) {
      await openSolve(d.slug);
    }
    if (state.duo && state.duo.session !== d.session) return; // superseded
    $("partner").hidden = false;
    $("partner-name").textContent = d.partner.name;
    $("partner-status").textContent = "live";
    partnerEd.set("", true);
    if (d.partnerState) applyPartner(d.partnerState);
    showLeftTab("duo");
    renderOnline();
    setMsg(`Duo with ${d.partner.name} on ${d.title || d.slug}. Your editor mirrors to them as you type.`);
    sendDuoUpdate(true);
  }
  function applyPartner(d) {
    const duo = state.duo; if (!duo) return;
    const changedLang = d.lang && d.lang !== duo.partnerLang;
    duo.partnerCode = d.code || ""; duo.partnerCursor = d.cursor || 0; duo.partnerLang = d.lang || duo.partnerLang;
    if (changedLang) partnerEd.setLang(duo.partnerLang);
    const scroll = $("partner-editor").querySelector(".editor-scroll");
    const st = scroll ? scroll.scrollTop : 0;
    if (partnerEd.value() !== duo.partnerCode) partnerEd.set(duo.partnerCode, true);
    partnerEd.setTints(duo.partnerCursor ? [{ start: duo.partnerCursor, end: duo.partnerCursor, cls: "partner" }] : []);
    if (scroll) scroll.scrollTop = st;
    $("partner-status").textContent = `live · ${duo.partnerLang || ""} · line ${duo.partnerCursor || 1}`;
  }
  let duoTimer = null;
  function sendDuoUpdate(now) {
    const duo = state.duo; if (!duo || duo.ended || !state.solve) return;
    clearTimeout(duoTimer);
    const send = () => {
      const ta = solveEd.ta;
      const cursor = ta.value.slice(0, ta.selectionStart).split("\n").length;
      api("POST", "/api/duo/update", { session: duo.session, code: ta.value, cursor, lang: state.solve.lang }).catch(() => {});
    };
    if (now) send(); else duoTimer = setTimeout(send, 120);
  }
  function endDuo(reason) {
    const duo = state.duo; if (!duo) return;
    duo.ended = true;
    state.duo = null;
    $("partner-status").textContent = "ended";
    $("btn-leave-duo").textContent = "close";
    renderOnline();
    if (reason) setMsg(reason);
  }
  async function leaveDuo() {
    if (state.duo) {
      const session = state.duo.session;
      endDuo("Left the duo");
      api("POST", "/api/duo/leave", { session }).catch(() => {});
    }
    $("partner").hidden = true;
    $("btn-leave-duo").textContent = "leave duo";
    partnerEd.set("", true);
    renderOnline();
  }
  $("btn-leave-duo").addEventListener("click", leaveDuo);
  $("btn-copy-partner").addEventListener("click", () => {
    if (!state.solve) return;
    const code = partnerEd.value();
    if (!code.trim()) return setMsg("Nothing to copy yet");
    solveEd.set(code, true);
    state.solve.loadedAttempt = 0;
    scheduleDraft(); renderEditorInfo(); sendDuoUpdate(false);
    setMsg("Copied your partner's code into your editor");
    setFocus("editor");
  });

  // ================================================================== settings (theme, account, admin)
  const THEMES = {
    coffee: { name: "Coffee (default)", vars: {
      "--bg": "#1d1712", "--bg-panel": "#221a14", "--cream": "#F1E3CB", "--tan": "#C9A87C", "--dim": "#8A7461",
      "--copper": "#D98E48", "--bark": "#6E4F37", "--mocha": "#4A3427", "--espresso": "#2C1E16",
      "--easy": "#A3B86C", "--medium": "#E2A93B", "--hard": "#C75D4A", "--err": "#E06C5A" } },
    midnight: { name: "Midnight", vars: {
      "--bg": "#12151f", "--bg-panel": "#171b28", "--cream": "#E6E9F2", "--tan": "#9AA6C4", "--dim": "#6B7590",
      "--copper": "#6F9BD8", "--bark": "#33405E", "--mocha": "#263252", "--espresso": "#1B2236",
      "--easy": "#7FC08A", "--medium": "#E0B24A", "--hard": "#E0685F", "--err": "#E0685F" } },
    forest: { name: "Forest", vars: {
      "--bg": "#101613", "--bg-panel": "#16211b", "--cream": "#E7F0E6", "--tan": "#9DB99F", "--dim": "#6E8A72",
      "--copper": "#7BBF6A", "--bark": "#33513A", "--mocha": "#24402C", "--espresso": "#16261B",
      "--easy": "#8FCE7E", "--medium": "#E0C14A", "--hard": "#D76A58", "--err": "#D76A58" } },
    nord: { name: "Nord", vars: {
      "--bg": "#2E3440", "--bg-panel": "#343B48", "--cream": "#ECEFF4", "--tan": "#A9B4C7", "--dim": "#7A869C",
      "--copper": "#88C0D0", "--bark": "#4C566A", "--mocha": "#3B4252", "--espresso": "#272C36",
      "--easy": "#A3BE8C", "--medium": "#EBCB8B", "--hard": "#BF616A", "--err": "#BF616A" } },
    plum: { name: "Plum", vars: {
      "--bg": "#191320", "--bg-panel": "#201829", "--cream": "#EFE6F2", "--tan": "#B89EC4", "--dim": "#856D94",
      "--copper": "#D98EC0", "--bark": "#4A3557", "--mocha": "#382742", "--espresso": "#241A2C",
      "--easy": "#A6CF6A", "--medium": "#E0B24A", "--hard": "#E0685F", "--err": "#E0685F" } },
    slate: { name: "Slate (mono)", vars: {
      "--bg": "#17181a", "--bg-panel": "#1d1e21", "--cream": "#E8E9EC", "--tan": "#A7ABB2", "--dim": "#71767E",
      "--copper": "#8AACCB", "--bark": "#3A3D42", "--mocha": "#2C2F34", "--espresso": "#212327",
      "--easy": "#9BC06B", "--medium": "#D8B457", "--hard": "#CE6A5A", "--err": "#CE6A5A" } },
  };
  let themeName = "slate";
  try { const t = localStorage.getItem("lc.theme"); if (t && THEMES[t]) themeName = t; } catch (_) { /* default */ }
  function applyTheme(name) {
    const t = THEMES[name] || THEMES.coffee;
    themeName = THEMES[name] ? name : "slate";
    const root = document.documentElement.style;
    for (const [k, v] of Object.entries(t.vars)) root.setProperty(k, v);
    try { localStorage.setItem("lc.theme", themeName); } catch (_) { /* private mode */ }
  }

  function openSettings() {
    $("theme-select").innerHTML = Object.entries(THEMES).map(([k, t]) => `<option value="${k}"${k === themeName ? " selected" : ""}>${esc(t.name)}</option>`).join("");
    const admin = state.me && state.me.id === 0;
    $("admin-section").hidden = !admin;
    if (admin) { renderStatus(); loadCacheStatus(); }
    const has = state.me && state.me.hasPassword;
    $("pw-current").hidden = !has;
    $("pw-confirm").value = ""; $("pw-new").value = ""; $("pw-current").value = "";
    $("pw-save").textContent = has ? "change password" : "set password";
    $("pw-note").textContent = has ? "" : "no password set — anyone can sign in as you";
    $("settings").hidden = false;
  }
  function closeSettings() { $("settings").hidden = true; }
  $("theme-select").addEventListener("change", (e) => applyTheme(e.target.value));
  $("settings-close").addEventListener("click", closeSettings);
  $("settings").addEventListener("click", (e) => { if (e.target === $("settings")) closeSettings(); });
  $("settings").addEventListener("keydown", (e) => { if (e.key === "Escape") { e.preventDefault(); closeSettings(); } e.stopPropagation(); });
  document.addEventListener("click", (e) => { if (e.target.closest(".settings-link")) openSettings(); });

  // Account dropdown in the top-right corner: Settings and Log out.
  function toggleUserMenu(open) {
    const menu = $("topbar-dropdown"), btn = $("topbar-user");
    const show = open === undefined ? menu.hidden : open;
    menu.hidden = !show;
    btn.setAttribute("aria-expanded", show ? "true" : "false");
  }
  async function doLogout() {
    try { await api("POST", "/api/logout"); } catch (_) { /* clear locally anyway */ }
    location.reload();
  }
  $("topbar-user").addEventListener("click", (e) => { e.stopPropagation(); toggleUserMenu(); });
  $("menu-settings").addEventListener("click", () => { toggleUserMenu(false); openSettings(); });
  $("menu-logout").addEventListener("click", () => { toggleUserMenu(false); doLogout(); });
  document.addEventListener("click", (e) => { if (!e.target.closest("#topbar-menu")) toggleUserMenu(false); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("topbar-dropdown").hidden) { e.stopPropagation(); toggleUserMenu(false); } }, true);

  applyTheme(themeName);

  // ================================================================== keys
  function cycleFocus(order, dir) {
    const i = order.indexOf(state.focus);
    setFocus(order[(i + dir + order.length) % order.length]);
  }
  document.addEventListener("keydown", (e) => {
    const typing = isTyping(e);
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.key === ",") { e.preventDefault(); if ($("settings").hidden) openSettings(); else closeSettings(); return; }
    if (!$("settings").hidden) return;

    if (state.view === "scratch") {
      const s = state.scratch; if (!s) return;
      if (mod && (e.key === "Enter" || e.key.toLowerCase() === "r")) { e.preventDefault(); runScratch(); return; }
      if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); flushScratch(); setMsg("Saved"); return; }
      if (mod && e.key.toLowerCase() === "l") { e.preventDefault(); $("scratch-lang-select").focus(); return; }
      if (e.ctrlKey && !e.metaKey && e.key === "`") { e.preventDefault(); setOutputHidden(!s.outputHidden); return; }
      if (typing) return;
      switch (e.key) {
        case "Escape": case "q": closeScratchView(); break;
        case "B": setOutputHidden(!s.outputHidden); break;
        case "n": newScratch(s.lang); break;
        case "i": case "Enter":
          if (state.focus === "scratches" && s.list[s.cursor] && s.list[s.cursor].id !== s.id) { flushScratch(); openScratch(s.list[s.cursor]); }
          setFocus("seditor"); e.preventDefault(); break;
        case "Tab": e.preventDefault(); cycleFocus(["scratches", "seditor", "output"], e.shiftKey ? -1 : 1); break;
        case "j": case "ArrowDown": case "k": case "ArrowUp": {
          const d = e.key === "j" || e.key === "ArrowDown" ? 1 : -1;
          if (state.focus === "scratches" && s.list.length) { s.cursor = Math.max(0, Math.min(s.list.length - 1, s.cursor + d)); s.confirmDelete = null; renderScratchList(); }
          else if (state.focus === "output") $("scratch-output").scrollTop += d * LH * 3;
          e.preventDefault(); break;
        }
        case "x": case "d": if (state.focus === "scratches" && s.list[s.cursor]) confirmDeleteScratch(s.list[s.cursor].id); break;
        case "y": if (state.focus === "scratches" && s.confirmDelete) deleteScratch(s.confirmDelete); break;
        case "r": if (state.focus === "output") runScratch(); break;
        case "h": case "ArrowLeft": if (state.focus !== "scratches") setFocus("scratches"); break;
        case "l": case "ArrowRight": if (state.focus === "scratches") setFocus("seditor"); break;
      }
      return;
    }

    if (state.view === "solve") {
      if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); saveAttempt(); return; }
      if (mod && e.key === "Enter") { e.preventDefault(); requestReview(false); return; }
      if (mod && e.key.toLowerCase() === "r") { e.preventDefault(); runSolve(); return; }
      if (mod && e.key.toLowerCase() === "l") { e.preventDefault(); $("lang").focus(); return; }
      if (e.ctrlKey && !e.metaKey && e.key === "`") { e.preventDefault(); state.bottomHiddenPref = !state.solve.bottomHidden; setBottomHidden(state.bottomHiddenPref); return; }
      // Left panel: ctrl+1 problem, ctrl+2 duo, ctrl+3 attempts (2 and 3 toggle back to the problem). Work while typing too.
      if (e.ctrlKey && !e.metaKey && !e.altKey && ["1", "2", "3"].includes(e.key)) {
        e.preventDefault();
        if (e.key === "1") showLeftTab("statement"); else toggleLeftTab(e.key === "2" ? "duo" : "attempts");
        return;
      }
      if (typing) return;
      const s = state.solve; if (!s) return;
      switch (e.key) {
        case "Escape": case "q": closeSolve(); break;
        case "P": showLeftTab("statement"); break;
        case "B": state.bottomHiddenPref = !s.bottomHidden; setBottomHidden(state.bottomHiddenPref); break;
        case "D": toggleLeftTab("duo"); break;
        case "A": toggleLeftTab("attempts"); break;
        case "i": case "Enter":
          if (state.focus === "statement" && s.leftTab === "attempts" && e.key === "Enter") loadAttempt(s.attempts[s.attemptCursor]);
          else if (state.focus === "bottom" && s.tab === "review" && e.key === "Enter") { const r = s.review; if (r && r.code === solveEd.value() && r.review.notes[s.noteCursor]) solveEd.gotoLine(r.review.notes[s.noteCursor].start); setFocus("editor"); }
          else setFocus("editor");
          e.preventDefault(); break;
        case "Tab": e.preventDefault(); cycleFocus(["statement", "editor", "bottom"], e.shiftKey ? -1 : 1); break;
        case "1": case "2": case "3": requestHint(+e.key, false); setFocus("bottom"); break;
        case "R": requestReview(false); setFocus("bottom"); break;
        case "E": showTab("examples"); setFocus("bottom"); break;
        case "r":
          if (state.focus === "bottom" && s.tab === "hint" && s.hintLevel) requestHint(s.hintLevel, true);
          else if (state.focus === "bottom" && s.tab === "review") requestReview(true);
          else if (state.focus === "bottom" && s.tab === "output") runSolve();
          break;
        case "j": case "ArrowDown": case "k": case "ArrowUp": {
          const d = e.key === "j" || e.key === "ArrowDown" ? 1 : -1;
          if (state.focus === "statement") {
            if (s.leftTab === "attempts" && s.attempts.length) { s.attemptCursor = Math.max(0, Math.min(s.attempts.length - 1, s.attemptCursor + d)); s.confirmDelete = null; renderAttempts(); }
            else if (s.leftTab === "duo") { const sc = $("partner-editor").querySelector(".editor-scroll"); if (sc) sc.scrollTop += d * LH * 3; }
            else $("statement").scrollTop += d * LH * 3;
          } else if (state.focus === "bottom") {
            if (s.tab === "review") moveNote(d);
            else $("bottom-body").scrollTop += d * LH * 3;
          }
          e.preventDefault(); break;
        }
        case "x": case "d":
          if (state.focus === "statement" && s.leftTab === "attempts" && s.attempts.length) {
            const id = s.attempts[s.attemptCursor].id;
            if (s.confirmDelete === id) { s.confirmDelete = null; deleteAttempt(id); }
            else { s.confirmDelete = id; renderAttempts(); setMsg(`Delete attempt #${id}? press x again to confirm`); }
          }
          break;
        case "y":
          if (state.focus === "statement" && s.confirmDelete) { const id = s.confirmDelete; s.confirmDelete = null; deleteAttempt(id); }
          break;
        case "h": case "ArrowLeft": if (state.focus !== "statement") setFocus("statement"); break;
        case "l": case "ArrowRight": if (state.focus === "statement") setFocus("editor"); break;
        case "g": if (state.focus === "statement") $("statement").scrollTop = 0; break;
        case "G": if (state.focus === "statement") $("statement").scrollTop = 1e9; break;
      }
      return;
    }

    // browse view
    if (typing) return;
    switch (e.key) {
      case "/": e.preventDefault(); setFocus("list"); $("search").focus(); break;
      case "S": openScratchView(); break;
      case "j": case "ArrowDown": case "k": case "ArrowUp": {
        const d = e.key === "j" || e.key === "ArrowDown" ? 1 : -1;
        if (state.focus === "filters") { state.sideCursor = Math.max(0, Math.min(state.sideItems.length - 1, state.sideCursor + d)); renderSidebar(); $("sidebar").querySelector(".item.cursor")?.scrollIntoView({ block: "nearest" }); }
        else if (state.focus === "preview") $("preview").scrollTop += d * LH * 3;
        else moveList(d);
        e.preventDefault(); break;
      }
      case "PageDown": case "PageUp": if (state.focus === "list") { moveList(e.key === "PageDown" ? 20 : -20); e.preventDefault(); } break;
      case "g": if (state.focus === "list") moveList(-1e9); break;
      case "G": if (state.focus === "list") moveList(1e9); break;
      case "Enter": case " ":
        if (state.focus === "filters") toggleSideItem();
        else if (selected()) openSolve(selected().slug);
        e.preventDefault(); break;
      case "Escape":
        if (state.focus === "filters") { state.difficulty = ""; state.topic = ""; state.statusFilter = ""; selectList(""); }
        else if (state.search) { state.search = ""; $("search").value = ""; applyFilters(); }
        break;
      case "Tab": e.preventDefault(); cycleFocus(["filters", "list", "preview"], e.shiftKey ? -1 : 1); break;
      case "h": case "ArrowLeft": if (state.focus === "list") setFocus("filters"); else if (state.focus === "preview") setFocus("list"); break;
      case "l": case "ArrowRight": if (state.focus === "filters") setFocus("list"); else if (state.focus === "list") setFocus("preview"); break;
      case "r": resync(); break;
      case "n": case "p": if (state.focus === "preview") moveList(e.key === "n" ? 1 : -1); break;
      case "?": setMsg("tab/←/→ panels · j/k move · enter solve · S scratchpad · / search · r resync · ctrl+, settings"); break;
    }
  });

  // ================================================================== footer
  function renderFooter() {
    const hint = (k, d) => `<b>${k}</b> ${d}`;
    const sep = "  ·  ";
    let parts, el;
    if (state.view === "browse") {
      el = $("browse-footer");
      if (document.activeElement === $("search")) parts = [hint("type", "filter"), hint("enter", "done"), hint("esc", "clear")];
      else if (state.focus === "filters") parts = [hint("j/k", "move"), hint("enter", "toggle"), hint("esc", "clear"), hint("tab", "next panel")];
      else if (state.focus === "preview") parts = [hint("j/k", "scroll"), hint("n/p", "next/prev"), hint("enter", "solve"), hint("o", "source"), hint("esc", "back")];
      else parts = [hint("j/k", "move"), hint("enter", "solve"), hint("S", "scratchpad"), hint("/", "search"), hint("o", "source"), hint("r", "resync"), hint("tab", "panel")];
    } else if (state.view === "scratch") {
      el = $("scratch-footer");
      if (state.focus === "seditor") parts = [hint("ctrl+enter", "run"), hint("ctrl+l", "language"), hint("ctrl+`", state.scratch && state.scratch.outputHidden ? "show output" : "hide output"), hint("tab", "indent"), hint("esc", "leave editor")];
      else if (state.focus === "output") parts = [hint("j/k", "scroll"), hint("r", "run again"), hint("tab", "next panel"), hint("q", "back to list")];
      else parts = [hint("j/k", "select"), hint("enter", "edit"), hint("n", "new"), hint("x", "delete"), hint("ctrl+enter", "run"), hint("q", "back to list")];
    } else {
      el = $("solve-footer");
      const s = state.solve;
      if (!s || s.loading) parts = [hint("esc", "back")];
      else if (state.focus === "editor") parts = [hint("ctrl+r", "run"), hint("ctrl+s", "save attempt"), hint("ctrl+enter", "review"), hint("ctrl+1/2/3", "problem/duo/attempts"), hint("ctrl+`", s.bottomHidden ? "show box" : "hide box"), hint("ctrl+l", "language"), hint("esc", "leave editor")];
      else if (state.focus === "bottom" && s.tab === "review") parts = [hint("j/k", "notes"), hint("enter", "go to line"), hint("r", "review again"), hint("1/2/3", "hints"), hint("q", "back to list")];
      else if (state.focus === "bottom" && s.tab === "hint") parts = [hint("j/k", "scroll"), hint("1/2/3", "hint level"), hint("r", "regenerate"), hint("R", "review"), hint("q", "back to list")];
      else if (state.focus === "bottom") parts = [hint("j/k", "scroll"), hint("r", "run again"), hint("tab", "editor"), hint("q", "back to list")];
      else if (s.leftTab === "attempts") parts = [hint("j/k", "select"), hint("enter", "load"), hint("x", "delete"), hint("P", "problem"), hint("D", "duo"), hint("tab", "editor"), hint("q", "back to list")];
      else if (s.leftTab === "duo") parts = [hint("j/k", "scroll"), hint("P", "problem"), hint("A", "attempts"), hint("i", "edit"), hint("tab", "editor"), hint("q", "back to list")];
      else parts = [hint("j/k", "scroll"), hint("i", "edit"), hint("D", "duo"), hint("A", "attempts"), hint("E", "examples"), hint("1/2/3", "hint"), hint("R", "review"), hint("q", "back to list")];
    }
    el.innerHTML = " " + parts.join(sep) + (state.msg ? `  ·  <span class="msg${state.msgErr ? " err" : ""}">${esc(state.msg)}</span>` : "");
    $("topbar-view").textContent = state.view === "browse" ? "problems" : state.view === "solve" ? (state.solve && state.solve.detail ? `${state.solve.detail.id}. ${state.solve.detail.title}` : "solve") : "scratchpad";
  }

  // ================================================================== init
  if (window.Prism && Prism.plugins && Prism.plugins.autoloader) {
    Prism.plugins.autoloader.languages_path = "https://cdnjs.cloudflare.com/ajax/libs/prism/1.29.0/components/";
  }
  renderFooter();
  async function boot() {
    api("GET", "/api/languages").then((d) => { state.languages = d.languages || []; state.runTimeout = d.timeoutSeconds || 10; renderStatus(); }).catch(() => {});
    loadStatus();
    api("GET", "/api/account").then((a) => { state.account = a; renderStatus(); loadCacheStatus(); }).catch(() => {});
    connectEvents();
    renderOnline();
    loadLists();
    await loadProblems().catch((err) => { setMsg("Error: " + err.message, true); $("rows").innerHTML = `<div class="err">${esc(err.message)}</div>`; });
    sendPresence();
  }
  (async () => {
    try {
      state.me = await fetch("/api/me").then((r) => (r.ok ? r.json() : null));
    } catch (_) { state.me = null; }
    if (!state.me) { showLogin(); loadStatus(); return; }
    await boot();
  })();
})();
