# AI-DLC Stage Grouping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an AI-DLC-mode sort option that groups documents by canonical workflow stage with `phase·step` numbers (1·2), phase color headers, and gray placeholders for stages that have no documents yet.

**Architecture:** Pure-frontend. A `aidlcClassify(relPath)` function maps each `aidlc-docs`-relative path to a canonical step key (from awslabs/aidlc-workflows). `renderAidlcGrouped()` renders the 13 canonical steps (always, empty ones dimmed) grouped under phase headers, reusing a `makeFileButton()` helper extracted from `renderFiles`. A segmented control (`단계순 / 최근수`) toggles between the grouped view and the existing flat list, persisted in localStorage.

**Tech Stack:** Embedded vanilla JS/CSS/HTML in `web.go` (single Go raw string — NO backticks in added JS; use double quotes).

Spec: `docs/superpowers/specs/2026-06-09-aidlc-stage-grouping-design.md`

---

## File Structure

- `web.go` only. All changes are in the embedded frontend:
  - JS: `AIDLC_STEPS`/`AIDLC_PHASES` tables, `aidlcClassify()`, `makeFileButton()` (extracted), `renderAidlcGrouped()`, sort-state + control wiring, `renderFilePane` branch.
  - HTML: a sort segmented control in the file-header.
  - CSS: `.aidlc-phase-head`, `.aidlc-step-head`, `.aidlc-step-empty`, `.aidlc-doc` indent, phase dots.
  - i18n: 4 keys in EN + KO maps.

No backend change → existing Go tests must still pass. Frontend verified in browser against the real folder `/Users/1111038/Desktop/ATDT_Tech/SON_AWS/dev/son-local-env/aidlc-docs`.

---

## Task 1: Classification tables + `aidlcClassify()`

**Files:**
- Modify: `web.go` — add near `renderFilePane` (~line 6066, before it).

- [ ] **Step 1: Add the canonical tables and classifier**

Insert this JS (double-quoted strings only):

```js
    // Canonical AI-DLC workflow steps (awslabs/aidlc-workflows). order = phase*100+step.
    var AIDLC_PHASES = {
      1: { emoji: "🔵", label: "INCEPTION", cls: "aidlc-p1" },
      2: { emoji: "🟢", label: "CONSTRUCTION", cls: "aidlc-p2" },
      3: { emoji: "🟡", label: "OPERATIONS", cls: "aidlc-p3" }
    };
    var AIDLC_STEPS = [
      { phase: 1, step: 1, key: "workspace",    label: "Workspace Detection" },
      { phase: 1, step: 2, key: "reverse",      label: "Reverse Engineering" },
      { phase: 1, step: 3, key: "requirements", label: "Requirements Analysis" },
      { phase: 1, step: 4, key: "stories",      label: "User Stories" },
      { phase: 1, step: 5, key: "planning",     label: "Workflow Planning" },
      { phase: 1, step: 6, key: "appdesign",    label: "Application Design" },
      { phase: 1, step: 7, key: "units",        label: "Units Generation" },
      { phase: 2, step: 1, key: "functional",   label: "Functional Design" },
      { phase: 2, step: 2, key: "nfrreq",       label: "NFR Requirements" },
      { phase: 2, step: 3, key: "nfrdesign",    label: "NFR Design" },
      { phase: 2, step: 4, key: "infra",        label: "Infrastructure Design" },
      { phase: 2, step: 5, key: "codegen",      label: "Code Generation" },
      { phase: 2, step: 6, key: "buildtest",    label: "Build & Test" },
      { phase: 3, step: 1, key: "operations",   label: "Operations" }
    ];

    // aidlcClassify maps an aidlc-docs-relative path to a step key (above),
    // "shared" (construction/shared-infrastructure.md), or "other" (unmatched).
    function aidlcClassify(rel) {
      var parts = String(rel || "").split("/");
      var leaf = parts[parts.length - 1];
      if (parts.length === 1) return "workspace"; // root-level file
      var p0 = parts[0];
      if (p0 === "inception") {
        var s = parts[1];
        if (s === "reverse-engineering") return "reverse";
        if (s === "requirements") return "requirements";
        if (s === "user-stories") return "stories";
        if (s === "plans") return "planning";
        if (s === "application-design") return leaf.indexOf("unit-of-work") === 0 ? "units" : "appdesign";
        return "other";
      }
      if (p0 === "construction") {
        var s2 = parts[1];
        if (leaf === "shared-infrastructure.md") return "shared";
        if (s2 === "build-and-test") return "buildtest";
        if (s2 === "plans") {
          if (leaf.indexOf("-functional-design-plan") >= 0) return "functional";
          if (leaf.indexOf("-nfr-requirements-plan") >= 0) return "nfrreq";
          if (leaf.indexOf("-nfr-design-plan") >= 0) return "nfrdesign";
          if (leaf.indexOf("-infrastructure-design-plan") >= 0) return "infra";
          if (leaf.indexOf("-code-generation-plan") >= 0) return "codegen";
          return "other";
        }
        var sf = parts[2]; // construction/{unit}/{stepFolder}/...
        if (sf === "functional-design") return "functional";
        if (sf === "nfr-requirements") return "nfrreq";
        if (sf === "nfr-design") return "nfrdesign";
        if (sf === "infrastructure-design") return "infra";
        if (sf === "code") return "codegen";
        return "other";
      }
      if (p0 === "operations") return "operations";
      return "other";
    }
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build -o mdviewer . 2>&1 | grep -v duplicate`
Expected: builds, no errors.

- [ ] **Step 3: Browser-verify classification against the real folder**

Start the server pointed at the real project, open the app, and in the browser console (or via the harness JS tool) run:
```js
[
  ["aidlc-state.md","workspace"],
  ["inception/requirements/requirements.md","requirements"],
  ["inception/application-design/components.md","appdesign"],
  ["inception/application-design/unit-of-work.md","units"],
  ["inception/plans/execution-plan.md","planning"],
  ["construction/cart/functional-design/business-rules.md","functional"],
  ["construction/plans/cart-nfr-design-plan.md","nfrdesign"],
  ["construction/build-and-test/build-instructions.md","buildtest"],
  ["construction/shared-infrastructure.md","shared"],
  ["operations/whatever.md","operations"],
  ["adr/ADR.md","other"],
  ["review/phase1-rq-decisions.md","other"]
].map(function(c){return c[0]+" => "+aidlcClassify(c[0])+(aidlcClassify(c[0])===c[1]?" OK":" MISMATCH("+c[1]+")");});
```
Expected: every line ends with `OK`.

- [ ] **Step 4: Commit**

```bash
git add web.go
git commit -m "feat(aidlc): canonical step tables + aidlcClassify path classifier"
```

---

## Task 2: Extract `makeFileButton()` from `renderFiles`

**Files:**
- Modify: `web.go` — `renderFiles` loop (~lines 5714-5737).

- [ ] **Step 1: Add `makeFileButton` and use it in `renderFiles`**

Replace the `for (const entry of filteredEntries) { … }` body (lines ~5714-5737) so the button construction is factored out. Add this helper just above `renderFiles`:

```js
    // makeFileButton builds one sidebar file/dir row. displayName overrides the
    // shown text (default entry.name); flag is the resolved flag class; aggregate
    // marks a dir whose flag comes from a changed child.
    function makeFileButton(entry, displayName, flag, aggregate) {
      const button = document.createElement("button");
      button.className = "file"
        + (entry.path === state.selectedPath ? " active" : "")
        + (flag ? " has-flag flag-" + flag : "")
        + (aggregate ? " flag-aggregate" : "");
      button.dataset.meta = describeEntryMeta(entry);
      if (flag) button.dataset.flag = flag;
      button.innerHTML = '<span class="file-name"></span><span class="file-meta"><span class="update-badge"></span><span class="file-size"></span></span>';
      button.querySelector(".file-name").innerHTML = fileNameHTML(displayName != null ? displayName : entry.name, state.searchQuery.trim());
      button.querySelector(".file-size").textContent = entry.is_dir ? "" : (function () {
        const ts = entryModTimestamp(entry);
        return ts ? relativeTime(ts) : "";
      })();
      button.onclick = () => entry.is_dir
        ? loadDir(entry.path, { historyMode: "push" })
        : selectFile(entry.path, { historyMode: "push" });
      return button;
    }
```

Then change the `renderFiles` loop body to:

```js
      for (const entry of filteredEntries) {
        const directFlag = state.fileFlags[entry.path] || "";
        const aggFlag = entry.is_dir ? (dirAggregateFlag[entry.path] || "") : "";
        const flag = directFlag || aggFlag;
        const aggregate = !!(aggFlag && !directFlag);
        filesEl.appendChild(makeFileButton(entry, entry.name, flag, aggregate));
      }
```

- [ ] **Step 2: Build + browser-verify no regression**

Run: `go build -o mdviewer . 2>&1 | grep -v duplicate`
Then open the app (normal folder mode) and confirm the file list still renders, flags/badges still show, and clicking a file still opens it. (Behavior-preserving refactor.)

- [ ] **Step 3: Commit**

```bash
git add web.go
git commit -m "refactor(files): extract makeFileButton from renderFiles"
```

---

## Task 3: `renderAidlcGrouped()` + CSS

**Files:**
- Modify: `web.go` — add `renderAidlcGrouped` near `renderFilePane`; add CSS near the `.aidlc-toggle` styles (~line 1772).

- [ ] **Step 1: Add the grouped renderer**

```js
    // renderAidlcGrouped draws the AI-DLC docs grouped by canonical phase/step,
    // numbered phase·step. All canonical steps are shown; empty steps are dimmed.
    function renderAidlcGrouped(files) {
      filesEl.innerHTML = "";
      // Bucket files by step key.
      var buckets = {}; // key -> [entry]
      (files || []).forEach(function (f) {
        var key = aidlcClassify(f.name);
        (buckets[key] = buckets[key] || []).push(f);
      });
      function leafName(name) {
        var i = name.lastIndexOf("/");
        return i >= 0 ? name.slice(i + 1) : name;
      }
      function appendDocs(entries) {
        entries.sort(function (a, b) { return a.name.localeCompare(b.name); });
        entries.forEach(function (entry) {
          var flag = state.fileFlags[entry.path] || "";
          var btn = makeFileButton(entry, leafName(entry.name), flag, false);
          btn.classList.add("aidlc-doc");
          btn.title = entry.name;
          filesEl.appendChild(btn);
        });
      }
      var lastPhase = 0;
      AIDLC_STEPS.forEach(function (st) {
        if (st.phase !== lastPhase) {
          lastPhase = st.phase;
          var ph = AIDLC_PHASES[st.phase];
          var phHead = document.createElement("div");
          phHead.className = "aidlc-phase-head " + ph.cls;
          phHead.innerHTML = '<span class="aidlc-dot"></span><span></span>';
          phHead.querySelector("span:last-child").textContent = ph.emoji + " " + ph.label;
          filesEl.appendChild(phHead);
          // Construction phase: show shared-infrastructure (2·0) first if present.
          if (st.phase === 2 && (buckets["shared"] || []).length) {
            var sh = document.createElement("div");
            sh.className = "aidlc-step-head";
            sh.innerHTML = '<span class="aidlc-step-num">2·0</span><span class="aidlc-step-name"></span><span class="aidlc-step-count"></span>';
            sh.querySelector(".aidlc-step-name").textContent = "Shared Infrastructure";
            sh.querySelector(".aidlc-step-count").textContent = "(" + buckets["shared"].length + ")";
            filesEl.appendChild(sh);
            appendDocs(buckets["shared"]);
          }
        }
        var docs = buckets[st.key] || [];
        var head = document.createElement("div");
        head.className = "aidlc-step-head" + (docs.length ? "" : " aidlc-step-empty");
        head.innerHTML = '<span class="aidlc-step-num"></span><span class="aidlc-step-name"></span><span class="aidlc-step-count"></span>';
        head.querySelector(".aidlc-step-num").textContent = st.phase + "·" + st.step;
        head.querySelector(".aidlc-step-name").textContent = st.label;
        head.querySelector(".aidlc-step-count").textContent = docs.length ? ("(" + docs.length + ")") : t("aidlcStepEmpty");
        filesEl.appendChild(head);
        appendDocs(docs);
      });
      // "Other" — anything unmatched (shown only when present).
      var other = buckets["other"] || [];
      if (other.length) {
        var oh = document.createElement("div");
        oh.className = "aidlc-step-head aidlc-other-head";
        oh.innerHTML = '<span class="aidlc-step-num">·</span><span class="aidlc-step-name"></span><span class="aidlc-step-count"></span>';
        oh.querySelector(".aidlc-step-name").textContent = t("aidlcOther");
        oh.querySelector(".aidlc-step-count").textContent = "(" + other.length + ")";
        filesEl.appendChild(oh);
        appendDocs(other);
      }
    }
```

- [ ] **Step 2: Add CSS**

Insert near the `.aidlc-toggle` rules (~line 1772):

```css
    .aidlc-phase-head { display: flex; align-items: center; gap: 6px; padding: 10px 12px 4px; font-size: 11px; font-weight: 700; letter-spacing: .05em; color: var(--muted); }
    .aidlc-phase-head .aidlc-dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
    .aidlc-phase-head.aidlc-p1 .aidlc-dot { background: oklch(0.62 0.19 264); }
    .aidlc-phase-head.aidlc-p2 .aidlc-dot { background: oklch(0.72 0.17 150); }
    .aidlc-phase-head.aidlc-p3 .aidlc-dot { background: oklch(0.80 0.15 85); }
    .aidlc-step-head { display: flex; align-items: baseline; gap: 6px; padding: 4px 12px 2px; font-size: 12px; color: var(--text); }
    .aidlc-step-num { font-variant-numeric: tabular-nums; font-weight: 600; color: var(--muted); min-width: 26px; }
    .aidlc-step-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .aidlc-step-count { font-size: 11px; color: var(--muted); }
    .aidlc-step-empty { opacity: 0.45; }
    .aidlc-doc { padding-left: 22px; }
```

- [ ] **Step 3: Build**

Run: `go build -o mdviewer . 2>&1 | grep -v duplicate`
Expected: builds.

- [ ] **Step 4: Commit**

```bash
git add web.go
git commit -m "feat(aidlc): grouped stage renderer with numbered steps + gray placeholders"
```

---

## Task 4: Sort control + state + wire `renderFilePane`

**Files:**
- Modify: `web.go` — HTML file-header (~line 4263), `state` (~line 4770), `renderFilePane` (~6068), `applyAidlcMode`/`updateAidlcToggle`, init.

- [ ] **Step 1: Add the segmented control markup**

In the `<div class="file-header">` (after the `aidlcToggle` button, line 4264), add:

```html
          <div class="search-sort aidlc-sort" id="aidlcSort" role="group" aria-label="AI-DLC sort" hidden>
            <button type="button" class="search-sort-btn active" id="aidlcSortStage" data-i18n="aidlcSortStage">단계순</button>
            <button type="button" class="search-sort-btn" id="aidlcSortRecent" data-i18n="aidlcSortRecent">최근수</button>
          </div>
```

- [ ] **Step 2: Add state + element refs + wiring**

In `state` (near `aidlcWanted`, ~line 4770) add:
```js
      aidlcSort: localStorage.getItem("mdviewer.aidlcSort") === "recent" ? "recent" : "stage",
```

Near `const aidlcToggleEl` (~line 4803) add:
```js
    const aidlcSortEl = document.getElementById("aidlcSort");
    const aidlcSortStageEl = document.getElementById("aidlcSortStage");
    const aidlcSortRecentEl = document.getElementById("aidlcSortRecent");
```

Change `renderFilePane` (~6068) to:
```js
    function renderFilePane() {
      if (state.aidlcMode && state.aidlc && state.aidlc.available) {
        if (state.aidlcSort === "stage") renderAidlcGrouped(state.aidlc.files || []);
        else renderFiles(state.aidlc.files || []);
      } else {
        renderFiles(state.entries);
      }
      updateAidlcSortControl();
    }
```

Add the control updater + handlers (near `updateAidlcToggle`, ~6064):
```js
    function updateAidlcSortControl() {
      if (!aidlcSortEl) return;
      var on = !!(state.aidlcMode && state.aidlc && state.aidlc.available);
      aidlcSortEl.hidden = !on;
      if (aidlcSortStageEl) aidlcSortStageEl.classList.toggle("active", state.aidlcSort === "stage");
      if (aidlcSortRecentEl) aidlcSortRecentEl.classList.toggle("active", state.aidlcSort === "recent");
    }
    function setAidlcSort(mode) {
      if (state.aidlcSort === mode) return;
      state.aidlcSort = mode;
      try { localStorage.setItem("mdviewer.aidlcSort", mode); } catch (e) {}
      renderFilePane();
    }
    if (aidlcSortStageEl) aidlcSortStageEl.addEventListener("click", function () { setAidlcSort("stage"); });
    if (aidlcSortRecentEl) aidlcSortRecentEl.addEventListener("click", function () { setAidlcSort("recent"); });
```

- [ ] **Step 3: Show/hide control when AI-DLC availability changes**

In `updateAidlcToggle` (~6064), add at the end (before its closing brace):
```js
      updateAidlcSortControl();
```

- [ ] **Step 4: CSS for the control placement**

Near the `.aidlc-*` CSS added in Task 3, add:
```css
    .aidlc-sort[hidden] { display: none; }
    .aidlc-sort { margin-left: auto; }
```

- [ ] **Step 5: Build**

Run: `go build -o mdviewer . 2>&1 | grep -v duplicate`
Expected: builds.

- [ ] **Step 6: Commit**

```bash
git add web.go
git commit -m "feat(aidlc): stage/recent sort toggle in AI-DLC mode (persisted)"
```

---

## Task 5: i18n labels (EN/KO)

**Files:**
- Modify: `web.go` — EN map (~line 6621) and KO map (~line 6739).

- [ ] **Step 1: Add keys to the EN map**

Near `aidlcToggleTitle` in the EN dict (~6621):
```js
        aidlcSortStage: "By stage", aidlcSortRecent: "Recent",
        aidlcStepEmpty: "(none yet)", aidlcOther: "Other",
```

- [ ] **Step 2: Add keys to the KO map**

Near `aidlcToggleTitle` in the KO dict (~6739):
```js
        aidlcSortStage: "단계순", aidlcSortRecent: "최근수",
        aidlcStepEmpty: "(아직 없음)", aidlcOther: "기타",
```

> If a third mirror of these keys exists, grep `grep -n "aidlcToggleTitle" web.go` and add the same keys there too.

- [ ] **Step 3: Build**

Run: `go build -o mdviewer . 2>&1 | grep -v duplicate`
Expected: builds.

- [ ] **Step 4: Commit**

```bash
git add web.go
git commit -m "feat(aidlc): i18n labels for stage sort + group headers (EN/KO)"
```

---

## Task 6: Full build + browser verification against the real folder

**Files:** none (verification).

- [ ] **Step 1: Full test + build**

Run: `go test ./... 2>&1 | grep -v duplicate && go build -o mdviewer .`
Expected: all Go tests PASS, build succeeds.

- [ ] **Step 2: Run the server against the real AI-DLC project**

Run: `./mdviewer --web --port 8475 --root /Users/1111038/Desktop/ATDT_Tech/SON_AWS/dev/son-local-env`
Open `http://127.0.0.1:8475/`, ensure the repo root with `aidlc-docs` loads (the AI-DLC toggle should appear).

- [ ] **Step 3: Verify the grouped view**

Turn on AI-DLC mode. Confirm:
- The `단계순 / 최근수` control appears; `단계순` is active by default.
- Phase headers 🔵 INCEPTION / 🟢 CONSTRUCTION / 🟡 OPERATIONS show with colored dots.
- Inception steps 1·1–1·7 show their documents (1·1 has aidlc-state.md/audit.md; 1·3 requirements; 1·6 application-design; 1·7 unit-of-work*).
- Construction 2·1–2·6 and Operations 3·1 are dimmed gray with "(아직 없음)".
- An "기타" group lists `adr/ADR.md` and `review/phase1-*.md`.
- Clicking a document opens it in the preview.
- Switching to `최근수` restores the flat most-recently-updated list; switching back to `단계순` regroups; the choice persists across reload.

- [ ] **Step 4: Verify normal mode unaffected**

Turn AI-DLC mode off → the control hides and the normal directory list shows as before.

- [ ] **Step 5: Final commit (if tweaks were needed)**

```bash
git add -A && git commit -m "test(aidlc): verification fixes"
```

---

## Self-Review Notes

- **Spec coverage:** classification table (T1), numbered grouped render + gray empty steps (T3), phase color headers (T3 CSS), sort option persisted + default stage (T4), i18n (T5), 기타/공통 handling (T3), no backend change (all tasks), browser verification on real folder (T6). All covered.
- **Type consistency:** `aidlcClassify` returns keys matching `AIDLC_STEPS[].key` plus `"shared"`/`"other"`. `makeFileButton(entry, displayName, flag, aggregate)` signature consistent across `renderFiles` (T2) and `renderAidlcGrouped` (T3). `state.aidlcSort` values `"stage"`/`"recent"` consistent in T4. Control id `aidlcSort`/`aidlcSortStage`/`aidlcSortRecent` consistent between markup (T4 Step 1) and refs (T4 Step 2).
- **Backtick safety:** all added JS uses double quotes; the only HTML-string literals use single/double quotes. No backticks introduced into `webAppHTML`.
- **i18n keys referenced before defined:** `t("aidlcStepEmpty")` and `t("aidlcOther")` used in T3 are defined in T5 — implement T5 before browser-testing T3/T6 (or the labels fall back to the key string harmlessly).
