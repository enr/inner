// Adds a "copy" button to the code blocks a reader would paste: shell commands
// and TOML config. Output samples, trees and Go excerpts are left alone.
(function () {
  "use strict";

  // Languages worth copying. "console" blocks mix commands and output, so
  // only their "$ " lines are copied, without the prompt.
  var COPYABLE = ["bash", "sh", "shell", "zsh", "fish", "powershell", "console", "toml"];
  var RESET_MS = 2000;

  var ICON_COPY =
    '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false">' +
    '<rect x="5.5" y="5.5" width="8" height="8" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.4"/>' +
    '<path d="M10.5 3.5v-.5A1.5 1.5 0 0 0 9 1.5H3A1.5 1.5 0 0 0 1.5 3v6A1.5 1.5 0 0 0 3 10.5h.5" fill="none" stroke="currentColor" stroke-width="1.4"/>' +
    "</svg>";
  var ICON_DONE =
    '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false">' +
    '<path d="M3 8.5l3 3 7-7" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>' +
    "</svg>";

  function snippetText(code, lang) {
    var text = code.textContent.replace(/\n+$/, "");
    if (lang !== "console") return text;
    return text
      .split("\n")
      .filter(function (line) { return /^\s*\$ /.test(line); })
      .map(function (line) { return line.replace(/^\s*\$ /, ""); })
      .join("\n");
  }

  function writeClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    // Fallback for plain-http previews, where the async API is unavailable.
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      ok ? resolve() : reject(new Error("copy failed"));
    });
  }

  function setState(btn, state) {
    var label = btn.querySelector(".copy-btn-label");
    btn.classList.toggle("is-copied", state === "copied");
    btn.classList.toggle("is-failed", state === "failed");
    if (state === "copied") {
      btn.querySelector(".copy-btn-icon").innerHTML = ICON_DONE;
      label.textContent = "copied";
    } else if (state === "failed") {
      label.textContent = "press ctrl+c";
    } else {
      btn.querySelector(".copy-btn-icon").innerHTML = ICON_COPY;
      label.textContent = "copy";
    }
  }

  function selectCode(code) {
    var range = document.createRange();
    range.selectNodeContents(code);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }

  function enhance(block) {
    var code = block.querySelector("pre > code[data-lang]");
    if (!code) return;
    var lang = code.getAttribute("data-lang").toLowerCase();
    if (COPYABLE.indexOf(lang) === -1) return;

    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy-btn";
    btn.setAttribute("aria-label", "Copy snippet to clipboard");
    btn.innerHTML =
      '<span class="copy-btn-icon">' + ICON_COPY + "</span>" +
      '<span class="copy-btn-label" aria-live="polite">copy</span>';

    var timer;
    btn.addEventListener("click", function () {
      clearTimeout(timer);
      writeClipboard(snippetText(code, lang)).then(
        function () { setState(btn, "copied"); },
        function () { selectCode(code); setState(btn, "failed"); }
      );
      timer = setTimeout(function () { setState(btn, "idle"); }, RESET_MS);
    });

    block.classList.add("has-copy");
    block.appendChild(btn);
  }

  function init() {
    var blocks = document.querySelectorAll(".doc-content .highlight");
    for (var i = 0; i < blocks.length; i++) enhance(blocks[i]);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
