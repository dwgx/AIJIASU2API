/* AIJIASU2API — control-plane landing page.
   Vanilla JS, no dependencies, no network calls.
   Chinese copy lives in the markup; English in the static #i18n-en block. */

(function () {
  "use strict";

  var root = document.documentElement;
  root.classList.add("js");

  var reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var STORAGE_KEY = "ajs-lang";

  /* ------------------------------------------------------------------ */
  /* Copy dictionary                                                     */
  /* ------------------------------------------------------------------ */

  var EN = {};
  try {
    EN = JSON.parse(document.getElementById("i18n-en").textContent);
  } catch (err) {
    EN = {};
  }

  var lang = "zh";
  try {
    var stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === "en" || stored === "zh") lang = stored;
  } catch (err) {
    /* storage unavailable; stay on the default */
  }

  var ZH_EXTRA = {
    "menu.open": "打开菜单",
    "menu.close": "关闭菜单",
    "code.copy": "复制",
    "code.copied": "已复制",
    "code.copyFail": "复制失败，请手动选择代码。"
  };

  var zhText = new Map();
  var zhHtml = new Map();
  var zhAria = new Map();
  var zhMeta = null;

  function metaContent(selector) {
    var el = document.querySelector(selector);
    return el ? el.getAttribute("content") || "" : "";
  }

  function captureChinese() {
    document.querySelectorAll("[data-i18n]").forEach(function (el) {
      zhText.set(el, el.textContent);
    });
    document.querySelectorAll("[data-i18n-html]").forEach(function (el) {
      zhHtml.set(el, el.innerHTML);
    });
    document.querySelectorAll("[data-i18n-aria]").forEach(function (el) {
      zhAria.set(el, el.getAttribute("aria-label") || "");
    });
    zhMeta = {
      title: document.title,
      desc: metaContent('meta[name="description"]'),
      ogTitle: metaContent('meta[property="og:title"]'),
      ogDesc: metaContent('meta[property="og:description"]'),
      twTitle: metaContent('meta[name="twitter:title"]'),
      twDesc: metaContent('meta[name="twitter:description"]'),
      imageAlt: metaContent('meta[property="og:image:alt"]')
    };
  }

  function t(key) {
    if (lang === "zh") return ZH_EXTRA[key] != null ? ZH_EXTRA[key] : "";
    return EN[key] != null ? EN[key] : "";
  }

  var menuButton = document.querySelector("[data-menu-toggle]");
  var nav = document.getElementById("site-nav");
  var header = document.querySelector("[data-header]");
  var copyStatus = document.getElementById("copy-status");

  function menuAria() {
    if (!menuButton) return;
    var open = header && header.getAttribute("data-open") === "true";
    menuButton.setAttribute("aria-label", t(open ? "menu.close" : "menu.open"));
  }

  function setMeta(selector, value) {
    var el = document.querySelector(selector);
    if (el) el.setAttribute("content", value);
  }

  function applyLang(next, persist) {
    lang = next === "en" ? "en" : "zh";
    root.lang = lang === "zh" ? "zh-CN" : "en";
    root.setAttribute("data-lang", lang);

    if (!zhMeta) captureChinese();

    if (lang === "zh") {
      document.querySelectorAll("[data-i18n]").forEach(function (el) {
        var value = zhText.get(el);
        if (value != null) el.textContent = value;
      });
      document.querySelectorAll("[data-i18n-html]").forEach(function (el) {
        var value = zhHtml.get(el);
        if (value != null) el.innerHTML = value;
      });
      document.querySelectorAll("[data-i18n-aria]").forEach(function (el) {
        var value = zhAria.get(el);
        if (value != null) el.setAttribute("aria-label", value);
      });
      document.title = zhMeta.title;
      setMeta('meta[name="description"]', zhMeta.desc);
      setMeta('meta[property="og:title"]', zhMeta.ogTitle);
      setMeta('meta[property="og:description"]', zhMeta.ogDesc);
      setMeta('meta[name="twitter:title"]', zhMeta.twTitle);
      setMeta('meta[name="twitter:description"]', zhMeta.twDesc);
      setMeta('meta[property="og:image:alt"]', zhMeta.imageAlt);
      setMeta('meta[property="og:locale"]', "zh_CN");
    } else {
      document.querySelectorAll("[data-i18n]").forEach(function (el) {
        var key = el.getAttribute("data-i18n");
        if (EN[key] != null) el.textContent = EN[key];
      });
      document.querySelectorAll("[data-i18n-html]").forEach(function (el) {
        var key = el.getAttribute("data-i18n-html");
        if (EN[key] != null) el.innerHTML = EN[key];
      });
      document.querySelectorAll("[data-i18n-aria]").forEach(function (el) {
        var key = el.getAttribute("data-i18n-aria");
        if (EN[key] != null) el.setAttribute("aria-label", EN[key]);
      });
      document.title = EN["meta.title"];
      setMeta('meta[name="description"]', EN["meta.desc"]);
      setMeta('meta[property="og:title"]', EN["og.title"]);
      setMeta('meta[property="og:description"]', EN["og.desc"]);
      setMeta('meta[name="twitter:title"]', EN["og.title"]);
      setMeta('meta[name="twitter:description"]', EN["og.desc"]);
      setMeta('meta[property="og:image:alt"]', EN["og.alt"]);
      setMeta('meta[property="og:locale"]', "en_US");
    }

    document.querySelectorAll("[data-copy]").forEach(function (btn) {
      btn.removeAttribute("data-state");
      var label = btn.querySelector("span");
      if (label) label.textContent = t("code.copy");
    });
    if (copyStatus) copyStatus.textContent = "";

    menuAria();

    if (persist) {
      try {
        window.localStorage.setItem(STORAGE_KEY, lang);
      } catch (err) {
        /* ignore */
      }
    }
  }

  /* ------------------------------------------------------------------ */
  /* Language toggle                                                     */
  /* ------------------------------------------------------------------ */

  document.querySelectorAll("[data-lang-toggle]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      applyLang(lang === "zh" ? "en" : "zh", true);
    });
  });

  /* ------------------------------------------------------------------ */
  /* Mobile navigation                                                   */
  /* ------------------------------------------------------------------ */

  function setMenu(open) {
    if (!header || !menuButton) return;
    header.setAttribute("data-open", open ? "true" : "false");
    menuButton.setAttribute("aria-expanded", open ? "true" : "false");
    menuAria();
  }

  if (menuButton && nav) {
    menuButton.addEventListener("click", function () {
      setMenu(header.getAttribute("data-open") !== "true");
    });

    nav.addEventListener("click", function (event) {
      if (event.target.closest("a")) setMenu(false);
    });

    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && header.getAttribute("data-open") === "true") {
        setMenu(false);
        menuButton.focus();
      }
    });

    document.addEventListener("click", function (event) {
      if (header.getAttribute("data-open") !== "true") return;
      if (!header.contains(event.target)) setMenu(false);
    });

    window.addEventListener("resize", function () {
      if (window.innerWidth > 960) setMenu(false);
    });
  }

  /* ------------------------------------------------------------------ */
  /* Tabs                                                               */
  /* ------------------------------------------------------------------ */

  document.querySelectorAll("[data-tabs]").forEach(function (group) {
    var tabs = Array.prototype.slice.call(group.querySelectorAll('[role="tab"]'));
    if (!tabs.length) return;

    var panels = tabs.map(function (tab) {
      return document.getElementById(tab.getAttribute("aria-controls"));
    });

    function select(index, moveFocus) {
      tabs.forEach(function (tab, i) {
        var on = i === index;
        tab.setAttribute("aria-selected", on ? "true" : "false");
        tab.tabIndex = on ? 0 : -1;
        if (panels[i]) panels[i].hidden = !on;
      });
      if (moveFocus) tabs[index].focus();
    }

    tabs.forEach(function (tab, index) {
      tab.addEventListener("click", function () {
        select(index, false);
      });

      tab.addEventListener("keydown", function (event) {
        var next = null;
        if (event.key === "ArrowRight" || event.key === "ArrowDown") {
          next = (index + 1) % tabs.length;
        } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
          next = (index - 1 + tabs.length) % tabs.length;
        } else if (event.key === "Home") {
          next = 0;
        } else if (event.key === "End") {
          next = tabs.length - 1;
        }
        if (next !== null) {
          event.preventDefault();
          select(next, true);
        }
      });
    });
  });

  /* ------------------------------------------------------------------ */
  /* Copy buttons                                                       */
  /* ------------------------------------------------------------------ */

  function legacyCopy(text) {
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.top = "-1000px";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    var ok = false;
    try {
      ok = document.execCommand("copy");
    } catch (err) {
      ok = false;
    }
    document.body.removeChild(area);
    return ok;
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(
        function () {
          return true;
        },
        function () {
          return legacyCopy(text);
        }
      );
    }
    return Promise.resolve(legacyCopy(text));
  }

  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var block = btn.closest(".code-block");
      var code = block ? block.querySelector("pre code") : null;
      if (!code) return;

      copyText(code.innerText).then(function (ok) {
        if (!ok) {
          if (copyStatus) copyStatus.textContent = t("code.copyFail");
          return;
        }
        var label = btn.querySelector("span");
        var path = block.querySelector(".code-path");
        btn.setAttribute("data-state", "done");
        if (label) label.textContent = t("code.copied");
        if (copyStatus) {
          copyStatus.textContent =
            t("code.copied") + (path ? " · " + path.textContent : "");
        }
        window.clearTimeout(btn._copyTimer);
        btn._copyTimer = window.setTimeout(function () {
          btn.removeAttribute("data-state");
          if (label) label.textContent = t("code.copy");
          if (copyStatus) copyStatus.textContent = "";
        }, 1800);
      });
    });
  });

  /* ------------------------------------------------------------------ */
  /* Scroll progress                                                     */
  /* ------------------------------------------------------------------ */

  var progress = document.querySelector("[data-progress]");
  var ticking = false;

  function updateProgress() {
    if (!progress) return;
    var max = document.documentElement.scrollHeight - window.innerHeight;
    var ratio = max > 0 ? Math.min(1, Math.max(0, window.scrollY / max)) : 0;
    progress.style.transform = "scaleX(" + ratio + ")";
    ticking = false;
  }

  if (progress) {
    window.addEventListener(
      "scroll",
      function () {
        if (!ticking) {
          ticking = true;
          window.requestAnimationFrame(updateProgress);
        }
      },
      { passive: true }
    );
    window.addEventListener("resize", updateProgress);
    updateProgress();
  }

  /* ------------------------------------------------------------------ */
  /* Reveal, counters, spotlight                                        */
  /* ------------------------------------------------------------------ */

  var supportsIO = "IntersectionObserver" in window;

  function scramble(el) {
    var target = parseInt(el.getAttribute("data-count"), 10);
    if (!isFinite(target) || target <= 0 || reduceMotion) {
      el.textContent = String(isFinite(target) ? target : 0);
      return;
    }

    var finalDigits = String(target).split("");
    var start = null;
    var duration = 820;

    function frame(now) {
      if (start === null) start = now;
      var p = Math.min(1, (now - start) / duration);
      var eased = 1 - Math.pow(1 - p, 3);
      var locked = Math.floor(eased * finalDigits.length);
      var out = "";

      for (var i = 0; i < finalDigits.length; i += 1) {
        if (i < locked) {
          out += finalDigits[i];
        } else {
          out += String(i === 0 ? 1 + Math.floor(Math.random() * 9) : Math.floor(Math.random() * 10));
        }
      }

      el.textContent = out;

      if (p < 1) {
        window.requestAnimationFrame(frame);
      } else {
        el.textContent = String(target);
      }
    }

    window.requestAnimationFrame(frame);
  }

  if (supportsIO) {
    var observer = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (entry) {
          if (!entry.isIntersecting) return;
          var el = entry.target;

          if (el.classList.contains("reveal")) el.classList.add("is-in");

          if (el.hasAttribute("data-count") && el.getAttribute("data-done") !== "1") {
            el.setAttribute("data-done", "1");
            scramble(el);
          }

          observer.unobserve(el);
        });
      },
      { rootMargin: "0px 0px -8% 0px", threshold: 0.12 }
    );

    document.querySelectorAll(".reveal").forEach(function (el) {
      var siblings = el.parentElement
        ? Array.prototype.slice.call(el.parentElement.children).filter(function (node) {
            return node.classList && node.classList.contains("reveal");
          })
        : [];
      var index = siblings.indexOf(el);
      el.style.setProperty("--d", Math.min(index < 0 ? 0 : index, 5) * 60 + "ms");
      observer.observe(el);
    });

    document.querySelectorAll("[data-count]").forEach(function (el) {
      observer.observe(el);
    });
  } else {
    document.querySelectorAll(".reveal").forEach(function (el) {
      el.classList.add("is-in");
    });
    document.querySelectorAll("[data-count]").forEach(function (el) {
      var target = parseInt(el.getAttribute("data-count"), 10);
      el.textContent = String(isFinite(target) ? target : 0);
    });
  }

  if (!reduceMotion && window.matchMedia("(hover: hover) and (pointer: fine)").matches) {
    document.querySelectorAll(".service, .bento-card").forEach(function (card) {
      var frame = 0;
      card.addEventListener("pointermove", function (event) {
        if (frame) return;
        frame = window.requestAnimationFrame(function () {
          var rect = card.getBoundingClientRect();
          card.style.setProperty("--mx", event.clientX - rect.left + "px");
          card.style.setProperty("--my", event.clientY - rect.top + "px");
          frame = 0;
        });
      });
    });
  }

  /* ------------------------------------------------------------------ */
  /* Boot                                                               */
  /* ------------------------------------------------------------------ */

  applyLang(lang, false);
})();
