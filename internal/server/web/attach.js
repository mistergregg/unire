"use strict";

// ---- images attached to a message ----
// The owner can attach images to the first message, a reply, or a message to a
// working agent: with the paperclip, or by pasting into the message box. They are
// sent base64-encoded with the message; the server checks them again (type, size,
// count), keeps them, and gives them to the agent (see sessions/attach.go).

const ATTACH_MAX = 5;
const ATTACH_SIZE = 5 * 1024 * 1024;
const ATTACH_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

function readDataURL(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(new Error("Could not read " + file.name));
    r.readAsDataURL(file);
  });
}

// attachPicker wires a file input, the button that opens it, the message box (for
// pasted images) and a row of previews. list() is what to send: [{name, data}].
function attachPicker(input, button, box, previews) {
  let picked = [];
  const render = () => {
    previews.hidden = picked.length === 0;
    previews.replaceChildren(...picked.map((p, i) => {
      const remove = el("button", { type: "button", class: "attach-remove", title: "Remove " + p.name, "aria-label": "Remove " + p.name }, "×");
      remove.addEventListener("click", () => { picked.splice(i, 1); render(); });
      return el("span", { class: "attach-preview", title: p.name }, el("img", { src: p.data, alt: p.name }), remove);
    }));
  };
  const add = async (files) => {
    for (const f of files) {
      if (picked.length >= ATTACH_MAX) { banner("Attach at most " + ATTACH_MAX + " images to a message.", true); break; }
      if (!ATTACH_TYPES.includes(f.type)) { banner(f.name + " is not a PNG, JPEG, GIF or WebP image.", true); continue; }
      if (f.size > ATTACH_SIZE) { banner(f.name + " is too big (5 MB at most).", true); continue; }
      try {
        picked.push({ name: f.name || "image", data: await readDataURL(f) });
      } catch (e) { banner(e.message, true); }
    }
    render();
  };
  button.addEventListener("click", () => input.click());
  input.addEventListener("change", () => { add([...input.files]); input.value = ""; });
  box.addEventListener("paste", (e) => {
    const files = [...(e.clipboardData ? e.clipboardData.files : [])].filter((f) => f.type.startsWith("image/"));
    if (!files.length) return;
    e.preventDefault();
    add(files);
  });
  return {
    list: () => picked.map((p) => ({ name: p.name, data: p.data })),
    set: (list) => { picked = list.slice(); render(); },
    clear: () => { picked = []; render(); },
  };
}

// attachThumbs shows images sent with a message: [{name, src, href?}].
function attachThumbs(list) {
  return list.map((a) => {
    const img = el("img", { src: a.src, alt: a.name, title: a.name, loading: "lazy" });
    return a.href ? el("a", { href: a.href, target: "_blank", rel: "noopener" }, img) : img;
  });
}
