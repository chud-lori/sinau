// Click handler for any element with `data-copy` whose value is the
// text to copy. After a successful copy the button briefly swaps its
// label (via `data-copied-label`) so the user gets feedback without
// needing an inline onclick handler — keeps the strict CSP happy.
//
// Usage in a template:
//   <button type="button" class="copy-btn" data-copy="{{.Code}}"
//           data-copied-label="Copied!">Copy</button>
document.addEventListener("click", function (e) {
  const btn = e.target.closest("[data-copy]");
  if (!btn) return;
  const text = btn.getAttribute("data-copy");
  if (!text) return;
  const done = function () {
    const original = btn.textContent;
    const next = btn.getAttribute("data-copied-label") || "Copied";
    btn.textContent = next;
    btn.classList.add("copied");
    setTimeout(function () {
      btn.textContent = original;
      btn.classList.remove("copied");
    }, 1500);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done, function () {
      // Permission denied / not available — fall through to the
      // textarea fallback below so older browsers still work.
      fallback();
    });
    return;
  }
  fallback();

  function fallback() {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "absolute";
    ta.style.left = "-9999px";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      done();
    } catch (err) {
      // No clipboard at all (e.g. insecure context). Surface the
      // original behaviour: user can still hand-select the visible
      // code.
    }
    document.body.removeChild(ta);
  }
});
