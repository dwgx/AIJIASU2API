/* AIJIASU2API landing page — progressive enhancement only.
   No framework, no external requests. Every control degrades gracefully. */

(function () {
  "use strict";

  document.documentElement.classList.add("js-reveal");

  /* Sticky header state. */
  var header = document.querySelector("[data-header]");
  var setHeaderState = function () {
    if (header) {
      header.classList.toggle("is-scrolled", window.scrollY > 8);
    }
  };
  setHeaderState();
  window.addEventListener("scroll", setHeaderState, { passive: true });

  /* Mobile navigation. */
  var menuButton = document.querySelector("[data-menu-toggle]");
  var nav = document.getElementById("site-nav");

  if (menuButton && nav) {
    var menuLabel = menuButton.querySelector(".menu-toggle-label");

    var setMenu = function (open) {
      menuButton.setAttribute("aria-expanded", String(open));
      nav.classList.toggle("is-open", open);
      if (menuLabel) {
        menuLabel.textContent = open ? "Close" : "Menu";
      }
    };

    menuButton.addEventListener("click", function () {
      setMenu(menuButton.getAttribute("aria-expanded") !== "true");
    });

    nav.addEventListener("click", function (event) {
      if (event.target.closest("a")) {
        setMenu(false);
      }
    });

    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && menuButton.getAttribute("aria-expanded") === "true") {
        setMenu(false);
        menuButton.focus();
      }
    });

    window.addEventListener("resize", function () {
      if (window.innerWidth > 860) {
        setMenu(false);
      }
    }, { passive: true });
  }

  /* Accessible tabs. */
  var tablist = document.querySelector('[role="tablist"]');

  if (tablist) {
    var tabs = Array.prototype.slice.call(tablist.querySelectorAll('[role="tab"]'));

    var selectTab = function (tab, moveFocus) {
      tabs.forEach(function (item) {
        var selected = item === tab;
        item.setAttribute("aria-selected", String(selected));
        item.tabIndex = selected ? 0 : -1;
        var panel = document.getElementById(item.getAttribute("aria-controls"));
        if (panel) {
          panel.hidden = !selected;
        }
      });
      if (moveFocus) {
        tab.focus();
      }
    };

    tabs.forEach(function (tab, index) {
      tab.addEventListener("click", function () {
        selectTab(tab, false);
      });

      tab.addEventListener("keydown", function (event) {
        var next = index;

        if (event.key === "ArrowRight") {
          next = (index + 1) % tabs.length;
        } else if (event.key === "ArrowLeft") {
          next = (index - 1 + tabs.length) % tabs.length;
        } else if (event.key === "Home") {
          next = 0;
        } else if (event.key === "End") {
          next = tabs.length - 1;
        } else {
          return;
        }

        event.preventDefault();
        selectTab(tabs[next], true);
      });
    });
  }

  /* Copy buttons, with a clipboard-API path and a selection fallback. */
  var status = document.getElementById("copy-status");
  var statusTimer = null;

  var announce = function (message) {
    if (!status) {
      return;
    }
    status.textContent = message;
    window.clearTimeout(statusTimer);
    statusTimer = window.setTimeout(function () {
      status.textContent = "";
    }, 2600);
  };

  var copyText = function (text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(function () {
        return true;
      }).catch(function () {
        return fallbackCopy(text);
      });
    }
    return Promise.resolve(fallbackCopy(text));
  };

  var fallbackCopy = function (text) {
    var area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.top = "-1000px";
    area.style.left = "0";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();

    var copied = false;
    try {
      copied = document.execCommand("copy");
    } catch (error) {
      copied = false;
    }
    area.remove();
    return copied;
  };

  Array.prototype.forEach.call(document.querySelectorAll("[data-copy]"), function (button) {
    button.addEventListener("click", function () {
      var block = button.closest(".code-block");
      var code = block ? block.querySelector("code") : null;

      if (!code) {
        return;
      }

      var original = button.textContent;
      copyText(code.textContent.trim()).then(function (copied) {
        button.classList.toggle("is-copied", copied);
        button.textContent = copied ? "Copied" : "Copy failed";
        announce(copied ? "Example copied to the clipboard." : "Copy failed. Select the code and copy it manually.");
        window.setTimeout(function () {
          button.textContent = original;
          button.classList.remove("is-copied");
        }, 2200);
      });
    });
  });

  /* Subtle scroll reveal. Content stays visible without JS. */
  var reveals = Array.prototype.slice.call(document.querySelectorAll(".reveal"));
  var reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  if (!("IntersectionObserver" in window) || reduceMotion.matches) {
    reveals.forEach(function (element) {
      element.classList.add("is-visible");
    });
  } else {
    var revealObserver = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (entry.isIntersecting) {
          entry.target.classList.add("is-visible");
          revealObserver.unobserve(entry.target);
        }
      });
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.08 });

    reveals.forEach(function (element) {
      revealObserver.observe(element);
    });

    var onMotionChange = function (event) {
      if (event.matches) {
        reveals.forEach(function (element) {
          element.classList.add("is-visible");
        });
      }
    };

    if (typeof reduceMotion.addEventListener === "function") {
      reduceMotion.addEventListener("change", onMotionChange);
    }
  }

  /* Highlight the section currently in view. */
  var navLinks = Array.prototype.slice.call(document.querySelectorAll('.nav-list a[href^="#"]'));
  var sections = navLinks.map(function (link) {
    return document.querySelector(link.getAttribute("href"));
  }).filter(Boolean);

  if (sections.length) {
    var setActiveSection = function () {
      var marker = window.scrollY + window.innerHeight * 0.35;
      var current = null;

      sections.forEach(function (section) {
        if (section.getBoundingClientRect().top + window.scrollY <= marker) {
          current = section;
        }
      });

      navLinks.forEach(function (link) {
        if (current && link.getAttribute("href") === "#" + current.id) {
          link.setAttribute("aria-current", "location");
        } else {
          link.removeAttribute("aria-current");
        }
      });
    };

    setActiveSection();
    window.addEventListener("scroll", setActiveSection, { passive: true });
    window.addEventListener("resize", setActiveSection, { passive: true });
  }
}());
