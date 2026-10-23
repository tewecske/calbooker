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
