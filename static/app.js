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
