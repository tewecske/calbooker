// Page behavior loaded from the same origin so the Content-Security-Policy can
// forbid inline scripts. It is loaded in <head> without defer so the saved
// theme applies before first paint.
(() => {
  const root = document.documentElement;
  const storageKey = "calbooker-theme";
  try {
    const serverTheme = root.dataset.accountTheme;
    const theme = serverTheme === "light" || serverTheme === "dark"
      ? serverTheme
      : localStorage.getItem(storageKey);
    if (theme === "light" || theme === "dark") {
      root.dataset.theme = theme;
    }
  } catch (_) {
    // Browser storage can be unavailable in privacy-restricted contexts.
  }

  const csrfToken = () => document.querySelector('meta[name="csrf-token"]')?.content || "";

  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:configRequest", (event) => {
      event.detail.headers["X-CSRF-Token"] = csrfToken();
    });

    const toggle = document.getElementById("theme-toggle");
    if (!toggle) return;
    const themeURL = root.dataset.themeUrl;
    toggle.checked = root.dataset.theme === "dark";
    toggle.addEventListener("change", () => {
      const theme = toggle.checked ? "dark" : "light";
      const previousTheme = theme === "dark" ? "light" : "dark";
      root.dataset.theme = theme;
      if (themeURL) {
        fetch(themeURL, {
          method: "POST",
          credentials: "same-origin",
          headers: {
            "Content-Type": "application/x-www-form-urlencoded",
            "X-CSRF-Token": csrfToken()
          },
          body: new URLSearchParams({theme}).toString()
        }).then((response) => {
          if (!response.ok) throw new Error("theme update rejected");
        }).catch(() => {
          root.dataset.theme = previousTheme;
          toggle.checked = previousTheme === "dark";
        });
        return;
      }
      try {
        localStorage.setItem(storageKey, theme);
      } catch (_) {
        // Keep current-page theme when browser storage is unavailable.
      }
    });
  });
})();

// Week grid drag-and-drop (templates/weekgrid.html). SortableJS, vendored in
// sortable.min.js, carries items between day columns; this code turns the
// drop point into a weekday, resource, and start minute snapped to the grid
// step, posts it with HTMX (the CSRF header comes from the listener above),
// and lets the server re-render the grid. The server stays the source of
// truth: an item dropped outside the grid goes back where it was.
(() => {
  let pointer = null;
  let grabOffset = 0;
  let suppressClickUntil = 0;

  const remember = (event) => {
    const point = (event.touches && event.touches[0]) || (event.changedTouches && event.changedTouches[0]) || event;
    if (typeof point.clientX === "number") pointer = {x: point.clientX, y: point.clientY};
  };
  for (const type of ["pointerdown", "pointermove", "mousemove", "touchstart", "touchmove", "touchend"]) {
    document.addEventListener(type, remember, {capture: true, passive: true});
  }

  const frame = (grid) => ({
    start: Number(grid.dataset.start),
    end: Number(grid.dataset.end),
    step: Number(grid.dataset.step),
    rows: Number(grid.dataset.rows),
  });

  const rowHeight = (column, f) => column.getBoundingClientRect().height / f.rows;

  const columnAt = (grid, x, y) => {
    for (const column of grid.querySelectorAll("[data-wg-column]")) {
      const r = column.getBoundingClientRect();
      if (x >= r.left && x < r.right && y >= r.top && y < r.bottom) return column;
    }
    return null;
  };

  const send = (grid, url, values) => {
    htmx.ajax("POST", url, {
      source: grid,
      target: grid,
      swap: "outerHTML",
      values: {...values, week: grid.dataset.week || ""},
    });
  };

  // Where a dragged item lands: the column under the pointer and the row of
  // the item's top edge, keeping its duration inside the visible rows.
  const dropTarget = (grid, item) => {
    if (!pointer) return null;
    const column = columnAt(grid, pointer.x, pointer.y);
    if (!column) return null;
    const f = frame(grid);
    const top = column.getBoundingClientRect().top;
    const duration = Number(item.dataset.end) - Number(item.dataset.start);
    const spanRows = Math.min(f.rows, Math.max(1, Math.ceil(duration / f.step)));
    const row = Math.round((pointer.y - grabOffset - top) / rowHeight(column, f));
    const start = f.start + Math.max(0, Math.min(row, f.rows - spanRows)) * f.step;
    return {weekday: column.dataset.weekday, resource: column.dataset.resource || "", start, end: start + duration};
  };

  const enableMove = (grid, url) => {
    for (const column of grid.querySelectorAll("[data-wg-column]")) {
      Sortable.create(column, {
        group: grid.id,
        sort: false,
        draggable: "[data-wg-draggable]",
        filter: "[data-wg-resize]",
        preventOnFilter: false,
        forceFallback: true,
        fallbackOnBody: true,
        fallbackTolerance: 3,
        // On touch screens a short hold starts a drag, so a swipe still
        // scrolls the grid sideways.
        delay: 200,
        delayOnTouchOnly: true,
        ghostClass: "wg-item-ghost",
        fallbackClass: "wg-item-drag",
        onChoose: (event) => {
          grabOffset = pointer ? pointer.y - event.item.getBoundingClientRect().top : 0;
        },
        onEnd: (event) => {
          suppressClickUntil = Date.now() + 400;
          const item = event.item;
          const target = dropTarget(grid, item);
          const unchanged = target &&
            target.weekday === item.dataset.weekday &&
            target.resource === (item.dataset.resource || "") &&
            target.start === Number(item.dataset.start);
          if (!target || unchanged) {
            event.from.appendChild(item);
            return;
          }
          send(grid, url, {
            item: item.dataset.wgItem,
            weekday: target.weekday,
            resource: target.resource,
            start: target.start,
            end: target.end,
          });
        },
      });
    }

    // Dragging the bottom edge changes the end time.
    grid.addEventListener("pointerdown", (event) => {
      const handle = event.target.closest("[data-wg-resize]");
      if (!handle) return;
      event.preventDefault();
      const item = handle.closest("[data-wg-item]");
      const column = item.closest("[data-wg-column]");
      const f = frame(grid);
      const top = column.getBoundingClientRect().top;
      const height = rowHeight(column, f);
      const startRow = Math.max(0, Math.floor((Number(item.dataset.start) - f.start) / f.step));
      const spanClasses = () => [...item.classList].filter((name) => name.startsWith("wg-rspan-"));
      const original = spanClasses();
      let endRow = null;
      const move = (moveEvent) => {
        endRow = Math.max(startRow + 1, Math.min(f.rows, Math.round((moveEvent.clientY - top) / height)));
        item.classList.remove(...spanClasses());
        item.classList.add(`wg-rspan-${endRow - startRow}`);
      };
      const finish = (finishEvent) => {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", finish);
        handle.removeEventListener("pointercancel", finish);
        suppressClickUntil = Date.now() + 400;
        const end = endRow === null ? null : f.start + endRow * f.step;
        if (finishEvent.type === "pointercancel" || end === null || end === Number(item.dataset.end)) {
          item.classList.remove(...spanClasses());
          item.classList.add(...original);
          return;
        }
        send(grid, url, {
          item: item.dataset.wgItem,
          weekday: item.dataset.weekday,
          resource: item.dataset.resource || "",
          start: item.dataset.start,
          end,
        });
      };
      handle.setPointerCapture(event.pointerId);
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", finish);
      handle.addEventListener("pointercancel", finish);
    });
  };

  // Clicking an empty slot adds an item of the default duration there.
  const enableCreate = (grid, url) => {
    grid.addEventListener("click", (event) => {
      if (Date.now() < suppressClickUntil) return;
      const column = event.target.closest("[data-wg-column]");
      if (!column || event.target !== column) return;
      const f = frame(grid);
      const row = Math.floor((event.clientY - column.getBoundingClientRect().top) / rowHeight(column, f));
      const start = f.start + Math.max(0, Math.min(row, f.rows - 1)) * f.step;
      const end = Math.min(f.end, start + Number(grid.dataset.defaultDuration || 60));
      send(grid, url, {weekday: column.dataset.weekday, resource: column.dataset.resource || "", start, end});
    });
  };

  const init = (root) => {
    if (typeof Sortable === "undefined" || !root || !root.querySelectorAll) return;
    const grids = root.matches("[data-week-grid]") ? [root] : root.querySelectorAll("[data-week-grid]");
    for (const grid of grids) {
      if (grid.dataset.wgReady) continue;
      grid.dataset.wgReady = "true";
      if (grid.dataset.moveUrl) enableMove(grid, grid.dataset.moveUrl);
      if (grid.dataset.createUrl) enableCreate(grid, grid.dataset.createUrl);
    }
  };

  document.addEventListener("DOMContentLoaded", () => init(document.documentElement));
  document.addEventListener("htmx:load", (event) => init(event.detail.elt));
})();

// Coach assignment board (templates/coach_board.html). SortableJS carries
// coach cards from the pool onto teams (as copies), between teams, and back
// to the pool (which takes them off the team). After a drop this code
// renames the card's form values to its new team and asks the server to
// re-render the board from the draft, so warnings and the "unsaved" state
// stay server-side. Nothing is saved until the user presses Save.
(() => {
  const refresh = (form) => {
    const button = form.querySelector("[data-cb-refresh]");
    if (button) button.click();
  };

  // Each card in a team list carries "a" = "<team>:<coach>" and its head
  // checkbox the same value; a card that changed team loses its head mark
  // and suggestion. A coach appears once per team.
  const sync = (form) => {
    for (const list of form.querySelectorAll("[data-cb-team]")) {
      const team = list.dataset.cbTeam;
      const seen = new Set();
      for (const card of [...list.querySelectorAll("[data-cb-coach]")]) {
        const coach = card.dataset.cbCoach;
        if (seen.has(coach)) {
          card.remove();
          continue;
        }
        seen.add(coach);
        const key = `${team}:${coach}`;
        const assignment = card.querySelector("[data-cb-assignment]");
        if (assignment) {
          assignment.value = key;
          assignment.disabled = false;
        }
        if (card.dataset.cbFrom !== team) {
          card.querySelector("[data-cb-suggested]")?.remove();
          const head = card.querySelector("[data-cb-head]");
          if (head) {
            head.checked = false;
            head.value = key;
          }
        }
      }
    }
  };

  const enable = (form) => {
    const group = `coach-board-${Math.random().toString(36).slice(2)}`;
    const common = {
      sort: false,
      draggable: "[data-cb-coach]",
      filter: "button, input, select, label, a",
      preventOnFilter: false,
      forceFallback: true,
      fallbackOnBody: true,
      fallbackTolerance: 3,
      delay: 200,
      delayOnTouchOnly: true,
      ghostClass: "cb-ghost",
      fallbackClass: "cb-drag",
    };
    const pool = form.querySelector("[data-cb-pool]");
    if (pool) {
      Sortable.create(pool, {
        ...common,
        group: {name: group, pull: "clone", put: true},
        onAdd: (event) => {
          // Dropped back on the pool: off the team.
          event.item.remove();
          refresh(form);
        },
      });
    }
    for (const list of form.querySelectorAll("[data-cb-team]")) {
      Sortable.create(list, {
        ...common,
        group: {name: group, pull: true, put: true},
        onAdd: () => {
          sync(form);
          refresh(form);
        },
      });
    }
    // At most one head coach per team.
    form.addEventListener("change", (event) => {
      const head = event.target.closest("[data-cb-head]");
      if (!head || !head.checked) return;
      const list = head.closest("[data-cb-team]");
      for (const other of list.querySelectorAll("[data-cb-head]")) {
        if (other !== head) other.checked = false;
      }
      refresh(form);
    });
  };

  const init = (root) => {
    if (typeof Sortable === "undefined" || !root || !root.querySelectorAll) return;
    const forms = root.matches("[data-coach-board]") ? [root] : root.querySelectorAll("[data-coach-board]");
    for (const form of forms) {
      if (form.dataset.cbReady) continue;
      form.dataset.cbReady = "true";
      enable(form);
    }
  };

  document.addEventListener("DOMContentLoaded", () => init(document.documentElement));
  document.addEventListener("htmx:load", (event) => init(event.detail.elt));
})();

// Training plan board (templates/training_plan.html). Team cards from the
// pool are dragged onto the week grid (the grid moves existing trainings
// itself, see above); this code turns the drop point into a weekday,
// resource, and start minute and asks the server to re-render the board from
// the draft held in the board's form. Switching the A/B week keeps unsaved
// changes the same way. Nothing is saved until the user presses Save.
(() => {
  let pointer = null;
  const remember = (event) => {
    const point = (event.touches && event.touches[0]) || (event.changedTouches && event.changedTouches[0]) || event;
    if (typeof point.clientX === "number") pointer = {x: point.clientX, y: point.clientY};
  };
  for (const type of ["pointerdown", "pointermove", "mousemove", "touchstart", "touchmove", "touchend"]) {
    document.addEventListener(type, remember, {capture: true, passive: true});
  }

  const send = (board, url, values) => {
    const form = board.querySelector("#training-plan-form");
    if (!form) return;
    htmx.ajax("POST", url, {source: form, target: board, swap: "outerHTML", values});
  };

  const dropOnGrid = (board, item) => {
    const grid = board.querySelector("[data-week-grid]");
    if (!grid || !pointer) return;
    const step = Number(grid.dataset.step);
    const first = Number(grid.dataset.start);
    const rows = Number(grid.dataset.rows);
    for (const column of grid.querySelectorAll("[data-wg-column]")) {
      const r = column.getBoundingClientRect();
      if (pointer.x < r.left || pointer.x >= r.right || pointer.y < r.top || pointer.y >= r.bottom) continue;
      const duration = Number(grid.dataset.defaultDuration || 60);
      const spanRows = Math.min(rows, Math.max(1, Math.ceil(duration / step)));
      const row = Math.floor((pointer.y - r.top) / (r.height / rows));
      const start = first + Math.max(0, Math.min(row, rows - spanRows)) * step;
      send(board, board.dataset.draftUrl, {
        add_team: item.dataset.tpTeam,
        weekday: column.dataset.weekday,
        resource: column.dataset.resource || "",
        start,
        end: start + spanRows * step,
        week: grid.dataset.week || "",
      });
      return;
    }
  };

  const enable = (board) => {
    const pool = board.querySelector("[data-tp-pool]");
    if (pool) {
      Sortable.create(pool, {
        group: {name: `training-plan-${Math.random().toString(36).slice(2)}`, pull: "clone", put: false},
        sort: false,
        draggable: "[data-tp-team]",
        filter: "button, input, select, label, a",
        preventOnFilter: false,
        forceFallback: true,
        fallbackOnBody: true,
        fallbackTolerance: 3,
        delay: 200,
        delayOnTouchOnly: true,
        ghostClass: "tp-ghost",
        fallbackClass: "tp-drag",
        onEnd: (event) => {
          event.clone?.remove();
          dropOnGrid(board, event.item);
        },
      });
    }
    // Switching the A/B week with unsaved changes posts the draft instead of
    // leaving the page.
    board.addEventListener("click", (event) => {
      const tab = event.target.closest("[data-wg-week-tab]");
      if (!tab || board.dataset.changed !== "true") return;
      event.preventDefault();
      const url = new URL(tab.href, window.location.href);
      url.pathname = `${url.pathname.replace(/\/$/, "")}/draft`;
      send(board, url.pathname + url.search, {});
    });
  };

  const init = (root) => {
    if (typeof Sortable === "undefined" || !root || !root.querySelectorAll) return;
    const boards = root.matches("[data-training-plan]") ? [root] : root.querySelectorAll("[data-training-plan]");
    for (const board of boards) {
      if (board.dataset.tpReady) continue;
      board.dataset.tpReady = "true";
      enable(board);
    }
  };

  document.addEventListener("DOMContentLoaded", () => init(document.documentElement));
  document.addEventListener("htmx:load", (event) => init(event.detail.elt));
})();

// Combobox (ARIA 1.2 combobox with listbox popup). Enhances a <select> or
// <select multiple> marked with data-combobox: the native select stays in the
// form, hidden, as the source of truth, so the form still submits without
// JavaScript and with the same field names and values. Multi-selects show
// their choices as removable chips. Texts come from data-combobox-* attributes
// (placeholder, empty, remove) so the server localizes them.
(() => {
  const fold = (text) => text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
  let counter = 0;

  const enhance = (select) => {
    if (select.dataset.comboboxReady) return;
    select.dataset.comboboxReady = "true";
    const multiple = select.multiple;
    const baseID = select.id || `combobox-${++counter}`;
    const inputID = `${baseID}-input`;
    const listID = `${baseID}-list`;
    const options = Array.from(select.options);
    const label = select.id ? document.querySelector(`label[for="${CSS.escape(select.id)}"]`) : null;

    const root = document.createElement("div");
    root.className = "relative";
    const field = document.createElement("div");
    field.className = "input combobox-field w-full h-auto min-h-10 flex-wrap items-center gap-1 py-1";
    const input = document.createElement("input");
    input.type = "text";
    input.id = inputID;
    input.className = "combobox-input grow min-w-24";
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-haspopup", "listbox");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-controls", listID);
    input.autocomplete = "off";
    input.placeholder = select.dataset.comboboxPlaceholder || "";
    const describedBy = select.getAttribute("aria-describedby");
    if (describedBy) input.setAttribute("aria-describedby", describedBy);
    const list = document.createElement("ul");
    list.id = listID;
    list.className = "combobox-list menu menu-sm absolute z-20 mt-1 w-full flex-nowrap overflow-y-auto rounded-box border border-base-300 bg-base-100 shadow-lg";
    list.setAttribute("role", "listbox");
    if (multiple) list.setAttribute("aria-multiselectable", "true");
    if (label) {
      label.setAttribute("for", inputID);
      label.id = label.id || `${baseID}-label`;
      list.setAttribute("aria-labelledby", label.id);
    }
    list.hidden = true;
    field.append(input);
    root.append(field, list);
    select.after(root);
    select.hidden = true;
    select.tabIndex = -1;
    select.removeAttribute("id");
    select.removeAttribute("aria-describedby");

    let shown = [];
    let active = -1;

    const open = () => {
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    };
    const close = () => {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
      active = -1;
    };
    const setActive = (index) => {
      active = index;
      list.querySelectorAll("[role=option]").forEach((item, position) => {
        item.classList.toggle("menu-active", position === index);
        if (position === index) {
          item.scrollIntoView({block: "nearest"});
          input.setAttribute("aria-activedescendant", item.id);
        }
      });
      if (index < 0) input.removeAttribute("aria-activedescendant");
    };

    const renderChips = () => {
      field.querySelectorAll(".combobox-chip").forEach((chip) => chip.remove());
      if (multiple) {
        for (const option of options.filter((item) => item.selected)) {
          const chip = document.createElement("span");
          chip.className = "combobox-chip badge badge-neutral gap-1";
          chip.dataset.value = option.value;
          chip.append(document.createTextNode(option.text));
          const remove = document.createElement("button");
          remove.type = "button";
          remove.className = "combobox-remove cursor-pointer";
          remove.tabIndex = -1;
          remove.textContent = "×";
          remove.setAttribute("aria-label", `${select.dataset.comboboxRemove || "Remove"} ${option.text}`);
          remove.addEventListener("click", () => {
            option.selected = false;
            changed();
            input.focus();
          });
          chip.append(remove);
          field.insertBefore(chip, input);
        }
        input.placeholder = options.some((item) => item.selected) ? "" : select.dataset.comboboxPlaceholder || "";
      } else {
        const chosen = options.find((item) => item.selected && item.value !== "");
        input.value = chosen ? chosen.text : "";
      }
    };

    const renderList = () => {
      const query = input.dataset.typed ? fold(input.value.trim()) : "";
      shown = options.filter((option) => !option.disabled && !(multiple && option.selected) && fold(option.text).includes(query));
      list.replaceChildren();
      shown.forEach((option, index) => {
        const item = document.createElement("li");
        item.id = `${baseID}-opt-${index}`;
        item.setAttribute("role", "option");
        item.setAttribute("aria-selected", String(option.selected));
        item.dataset.value = option.value;
        const text = document.createElement("a");
        text.textContent = option.text;
        item.append(text);
        item.addEventListener("mousedown", (event) => {
          event.preventDefault();
          choose(option);
        });
        list.append(item);
      });
      if (shown.length === 0) {
        const empty = document.createElement("li");
        empty.className = "menu-disabled";
        empty.setAttribute("role", "option");
        empty.setAttribute("aria-disabled", "true");
        empty.id = `${baseID}-empty`;
        const text = document.createElement("span");
        text.textContent = select.dataset.comboboxEmpty || "";
        empty.append(text);
        list.append(empty);
      }
      setActive(shown.length > 0 ? 0 : -1);
    };

    const changed = () => {
      renderChips();
      select.dispatchEvent(new Event("change", {bubbles: true}));
    };

    const choose = (option) => {
      option.selected = true;
      delete input.dataset.typed;
      if (multiple) {
        input.value = "";
        changed();
        renderList();
        open();
      } else {
        changed();
        close();
      }
    };

    input.addEventListener("input", () => {
      input.dataset.typed = "true";
      renderList();
      open();
    });
    input.addEventListener("focus", () => {
      if (!multiple) input.select();
    });
    input.addEventListener("blur", () => {
      close();
      delete input.dataset.typed;
      input.value = "";
      renderChips();
    });
    field.addEventListener("mousedown", (event) => {
      if (event.target !== input && !event.target.closest(".combobox-remove")) {
        event.preventDefault();
        input.focus();
      }
    });
    input.addEventListener("click", () => {
      if (list.hidden) {
        renderList();
        open();
      }
    });
    input.addEventListener("keydown", (event) => {
      switch (event.key) {
        case "ArrowDown":
        case "ArrowUp": {
          event.preventDefault();
          if (list.hidden) {
            renderList();
            open();
            return;
          }
          if (shown.length === 0) return;
          const step = event.key === "ArrowDown" ? 1 : -1;
          setActive((active + step + shown.length) % shown.length);
          break;
        }
        case "Enter":
          if (!list.hidden && active >= 0 && shown[active]) {
            event.preventDefault();
            choose(shown[active]);
          } else if (!list.hidden || input.value !== "") {
            event.preventDefault();
          }
          break;
        case "Escape":
          if (!list.hidden) {
            event.preventDefault();
            event.stopPropagation();
            close();
          }
          break;
        case "Backspace":
          if (multiple && input.value === "") {
            const selected = options.filter((item) => item.selected);
            if (selected.length > 0) {
              selected[selected.length - 1].selected = false;
              changed();
              if (!list.hidden) renderList();
            }
          }
          break;
        case "Tab":
          close();
          break;
      }
    });
    select.form?.addEventListener("reset", () => setTimeout(renderChips));
    renderChips();
  };

  const init = (scope) => {
    scope.querySelectorAll?.("select[data-combobox]").forEach(enhance);
    if (scope.matches?.("select[data-combobox]")) enhance(scope);
  };
  document.addEventListener("DOMContentLoaded", () => {
    init(document);
    document.body.addEventListener("htmx:load", (event) => init(event.target));
  });
})();
