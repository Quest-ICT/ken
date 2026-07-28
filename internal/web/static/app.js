/* ==========================================================================
   Ken — progressive enhancement. ONE external script (strict CSP:
   default-src 'self'). All behaviour is delegated off document and keyed to
   data-* attributes; the app is fully usable with JS disabled.

   Adapted from the Claude Design handoff for Ken's server-rendered model:
   theme + language persist in COOKIES the server reads (so the initial paint
   is correct with no flash and no client-side theme restore), and the
   language <select> drives Ken's /lang round-trip.
   ========================================================================== */
(function () {
  "use strict";

  var root = document.documentElement;
  root.classList.remove("no-js");
  root.classList.add("js");

  function cookie(name, value) {
    var secure = location.protocol === "https:" ? "; Secure" : "";
    document.cookie = name + "=" + value + "; Path=/; Max-Age=31536000; SameSite=Lax" + secure;
  }

  function effectiveTheme() {
    var set = root.getAttribute("data-theme");
    if (set === "light" || set === "dark") return set;
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }

  function toggleTheme() {
    var next = effectiveTheme() === "dark" ? "light" : "dark";
    root.setAttribute("data-theme", next);
    cookie("ken_theme", next); // the server renders data-theme from this on the next request
  }

  /* ---- Copy to clipboard ------------------------------------------------- */
  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) return navigator.clipboard.writeText(text);
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.top = "-1000px";
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand("copy"); resolve(); } catch (e) { reject(e); }
      finally { document.body.removeChild(ta); }
    });
  }

  function handleCopy(btn) {
    var text = btn.getAttribute("data-copy-text");
    if (!text) {
      var sel = btn.getAttribute("data-copy-target");
      if (sel) { var el = document.querySelector(sel); if (el) text = (el.innerText || el.textContent || "").trim(); }
    }
    if (!text) return;
    copyText(text).then(function () {
      btn.setAttribute("data-copied", "true");
      var live = document.getElementById("copy-live");
      if (live) live.textContent = btn.getAttribute("data-copied-label") || "Copied to clipboard";
      window.clearTimeout(btn._t);
      btn._t = window.setTimeout(function () { btn.removeAttribute("data-copied"); }, 1800);
    }).catch(function () {});
  }

  /* ---- Delegated click --------------------------------------------------- */
  document.addEventListener("click", function (ev) {
    var t = ev.target.closest ? ev.target.closest("[data-action]") : null;
    if (!t) return;
    var action = t.getAttribute("data-action");
    if (action === "toggle-theme") { ev.preventDefault(); toggleTheme(); }
    else if (action === "copy") { ev.preventDefault(); handleCopy(t); }
    else if (action === "dismiss-flash") { var f = t.closest(".flash"); if (f) f.remove(); }
    else if (action === "close-nav") { var cb = document.getElementById("navtoggle"); if (cb) cb.checked = false; }
    else if (action === "toggle-pw") {
      ev.preventDefault();
      var target = document.querySelector(t.getAttribute("data-pw-target"));
      if (!target) return;
      var show = target.getAttribute("type") === "password";
      target.setAttribute("type", show ? "text" : "password");
      t.setAttribute("aria-pressed", show ? "true" : "false");
      t.setAttribute("aria-label", show ? t.getAttribute("data-label-hide") || "Hide password"
                                        : t.getAttribute("data-label-show") || "Show password");
    }
  });

  /* ---- Language selector: submit its form → /lang (server sets the cookie
         and redirects back). With JS off, the form's submit button is shown. -- */
  document.addEventListener("change", function (ev) {
    var t = ev.target.closest ? ev.target.closest('[data-action="lang"]') : null;
    if (t && t.form) t.form.submit();
  });

  /* ---- Confirm-before-submit (CSP-clean; inline onsubmit is blocked) ------ */
  document.addEventListener("submit", function (ev) {
    var f = ev.target;
    var msg = f && f.getAttribute ? f.getAttribute("data-confirm") : null;
    if (msg && !window.confirm(msg)) ev.preventDefault();
  });

  /* ---- Back-to-top visibility + close mobile nav on Escape --------------- */
  var btt = null;
  function onScroll() { if (btt) btt.classList.toggle("is-visible", (window.pageYOffset || 0) > 400); }

  // The page's language, used for BOTH date formatting and relative-time wording, so
  // the browser localizes them for free (no message keys of our own to keep in sync).
  function pageLang() {
    return document.documentElement.getAttribute("lang") || undefined;
  }

  // Render every <time data-localtime> the server emitted into the VIEWER's timezone.
  //
  // Timestamps are STORED and SENT as UTC (the datetime attribute keeps them that way,
  // machine-readable and unambiguous); only what a human reads is converted. Getting
  // this backwards is a real hazard: an unmarked UTC time reads as local and silently
  // shifts a deadline by the reader's offset.
  //
  // data-localtime="relative" is for DEADLINES and expiries — "in 8 minutes" needs no
  // timezone, no clock arithmetic, and cannot be misread; the exact local time stays
  // available on hover.
  function renderLocalTimes() {
    var lang = pageLang();
    var els = document.querySelectorAll("time[data-localtime]");
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      var d = new Date(el.getAttribute("datetime"));
      if (isNaN(d.getTime())) continue;              // leave the server fallback in place
      var abs = d.toLocaleString(lang, { dateStyle: "short", timeStyle: "short" });
      if (el.getAttribute("data-localtime") === "relative") {
        el.textContent = relativeTime(d, lang) || abs;
        el.title = abs;                               // exact local time on hover
      } else {
        el.textContent = abs;
        el.title = el.getAttribute("datetime");       // the machine-facing UTC on hover
      }
    }
  }

  // "in 8 minutes" / "3 hours ago", localized by the browser. Returns "" when
  // Intl.RelativeTimeFormat is unavailable so the caller falls back to absolute.
  function relativeTime(d, lang) {
    if (typeof Intl === "undefined" || !Intl.RelativeTimeFormat) return "";
    var ms = d.getTime() - Date.now();
    var abs = Math.abs(ms);
    var units = [["day", 86400000], ["hour", 3600000], ["minute", 60000], ["second", 1000]];
    var rtf = new Intl.RelativeTimeFormat(lang, { numeric: "auto" });
    for (var i = 0; i < units.length; i++) {
      if (abs >= units[i][1] || i === units.length - 1) {
        return rtf.format(Math.round(ms / units[i][1]), units[i][0]);
      }
    }
    return "";
  }

  // Stamp every [data-live-checked] element with the current local time. Called on
  // load and after each successful poll, so the "last checked" line the user reads
  // reflects the browser's own clock and timezone — not the server's.
  function stampChecked() {
    var t = new Date().toLocaleString(pageLang(), { dateStyle: "short", timeStyle: "medium" });
    var els = document.querySelectorAll("[data-live-checked]");
    for (var i = 0; i < els.length; i++) { els[i].textContent = t; }
  }

  // Live refresh for a page that carries [data-live-refresh="<n>"] (Proposals and,
  // when enabled, Comm). The page is polled at <path>/count — derived from the
  // current path, so no per-page URL is hard-coded — and reloaded when the server's
  // count diverges from the value the page was rendered with, so a curator who
  // leaves the page open sees new proposals or a peer session connect without a
  // manual refresh (and stays in sync when something is actioned in another tab).
  // Every successful poll also re-stamps the "last checked" line, so the user can
  // see the page is genuinely being re-checked even when nothing has changed.
  // Same-origin fetch, so it satisfies the strict default-src 'self' CSP; skipped
  // while the tab is hidden, and checked immediately when the tab regains focus.
  function startLiveRefresh() {
    var marker = document.querySelector("[data-live-refresh]");
    if (!marker) return;
    var shown = parseInt(marker.getAttribute("data-live-refresh"), 10);
    if (isNaN(shown)) return;
    var url = window.location.pathname.replace(/\/+$/, "") + "/count";
    var checking = false;
    function check() {
      if (document.hidden || checking) return;
      checking = true;
      fetch(url, { headers: { Accept: "application/json" }, credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) {
          // A successful JSON response means the server was reached: re-stamp the
          // "last checked" line whether or not anything changed. Reload only on a
          // real numeric change. A session that expired server-side redirects this
          // fetch to the HTML login page, whose body is not JSON — r.json() throws,
          // the catch swallows it, no stamp update and no reload loop results.
          if (d && typeof d.count === "number") {
            stampChecked();
            if (d.count !== shown) window.location.reload();
          }
        })
        .catch(function () {})
        .then(function () { checking = false; });
    }
    window.setInterval(check, 20000);
    document.addEventListener("visibilitychange", function () { if (!document.hidden) check(); });
  }

  document.addEventListener("DOMContentLoaded", function () {
    btt = document.querySelector("[data-backtotop]");
    if (btt) { onScroll(); window.addEventListener("scroll", onScroll, { passive: true }); }
    renderLocalTimes();
    // Relative deadlines go stale as they sit; re-render them on a slow tick so an
    // open page never shows "in 2 minutes" ten minutes later.
    window.setInterval(renderLocalTimes, 30000);
    stampChecked();
    startLiveRefresh();
  });
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") { var cb = document.getElementById("navtoggle"); if (cb && cb.checked) cb.checked = false; }
  });
})();
