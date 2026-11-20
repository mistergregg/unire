"use strict";

// ---- files attached to a message ----
// The owner can attach files to the first message, a reply, or a message to a
// working agent: with the paperclip, by pasting files into the message box, or by
// pasting a long text, which becomes a text file instead of filling the box. They
// are sent base64-encoded with the message; the server checks them again (size,
// count), keeps them, and gives them to the agent (see sessions/attach.go).
// Images show as thumbnails, other files as a chip with their name.

const ATTACH_MAX = 5;
const ATTACH_SIZE = 5 * 1024 * 1024;
const ATTACH_IMAGES = ["image/png", "image/jpeg", "image/gif", "image/webp"];
const PASTE_AS_FILE = 4000; // pasted text longer than this (in characters) is attached as a file

const isImageName = (name) => /\.(png|jpe?g|gif|webp)$/i.test(name);

function readDataURL(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(new Error("Could not read " + file.name));
    r.readAsDataURL(file);
  });
}

// attachPicker wires a file input, the button that opens it, the message box (for
// pasted files and long texts) and a row of previews. list() is what to send:
// [{name, data, image}].
function attachPicker(input, button, box, previews) {
  let picked = [];
  let pastes = 0;
  const render = () => {
    previews.hidden = picked.length === 0;
    previews.replaceChildren(...picked.map((p, i) => {
      const remove = el("button", { type: "button", class: "attach-remove", title: "Remove " + p.name, "aria-label": "Remove " + p.name }, "×");
      remove.addEventListener("click", () => { picked.splice(i, 1); render(); });
      const look = p.image ? el("img", { src: p.data, alt: p.name }) : el("span", { class: "attach-file-name" }, p.name);
      return el("span", { class: "attach-preview" + (p.image ? "" : " file"), title: p.name }, look, remove);
    }));
  };
  const add = async (files) => {
    for (const f of files) {
      if (picked.length >= ATTACH_MAX) { banner("Attach at most " + ATTACH_MAX + " files to a message.", true); break; }
      if (!f.size) { banner((f.name || "That file") + " is empty.", true); continue; }
      if (f.size > ATTACH_SIZE) { banner(f.name + " is too big (5 MB at most).", true); continue; }
      try {
        picked.push({ name: f.name || "file", data: await readDataURL(f), image: ATTACH_IMAGES.includes(f.type) });
      } catch (e) { banner(e.message, true); }
    }
    render();
  };
  button.addEventListener("click", () => input.click());
  input.addEventListener("change", () => { add([...input.files]); input.value = ""; });
  box.addEventListener("paste", (e) => {
    const cd = e.clipboardData;
    if (!cd) return;
    const files = [...cd.files];
    if (files.length) {
      e.preventDefault();
      add(files);
      return;
    }
    const text = cd.getData("text/plain");
    if (text.length > PASTE_AS_FILE) {
      e.preventDefault();
      pastes++;
      add([new File([text], "pasted text" + (pastes > 1 ? " " + pastes : "") + ".txt", { type: "text/plain" })]);
    }
  });
  return {
    list: () => picked.map((p) => ({ name: p.name, data: p.data, image: p.image })),
    set: (list) => { picked = list.slice(); render(); },
    clear: () => { picked = []; render(); },
  };
}

// attachThumbs shows files sent with a message: [{name, src, image, href?}].
// Images are thumbnails; other files a chip that downloads them.
function attachThumbs(list) {
  return list.map((a) => {
    if (!a.image) {
      const chip = el("span", { class: "attach-chip", title: a.name }, a.name);
      return a.href ? el("a", { href: a.href, download: a.name, class: "attach-chip-link" }, chip) : chip;
    }
    const img = el("img", { src: a.src, alt: a.name, title: a.name, loading: "lazy" });
    return a.href ? el("a", { href: a.href, target: "_blank", rel: "noopener" }, img) : img;
  });
}
