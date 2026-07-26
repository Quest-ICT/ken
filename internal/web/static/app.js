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
  document.addEventListener("DOMContentLoaded", function () {
    btt = document.querySelector("[data-backtotop]");
    if (btt) { onScroll(); window.addEventListener("scroll", onScroll, { passive: true }); }
  });
  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Escape") { var cb = document.getElementById("navtoggle"); if (cb && cb.checked) cb.checked = false; }
  });
})();
