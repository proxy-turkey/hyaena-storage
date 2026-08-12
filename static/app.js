/* Hyaena Storage — upload sayfası mantığı (bulk: sınırlı paralel kuyruk) */
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const dropzone = $("dropzone");
  const fileInput = $("file-input");

  // Kuyruk: aynı anda en fazla MAX_CONCURRENT dosya yüklenir.
  const MAX_CONCURRENT = 2;
  let queue = []; // { file, status:'waiting'|'uploading'|'done'|'failed', token, safeName, partCount, segmentBytes, xhr, error }
  let activeCount = 0;
  let cancelled = false;

  function fmt(bytes) {
    if (bytes === 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }

  function showError(msg) {
    const box = $("error-box");
    box.textContent = msg;
    box.classList.remove("hidden");
  }
  function clearError() {
    $("error-box").classList.add("hidden");
    $("error-box").textContent = "";
  }

  // ---------- Aşama geçişleri ----------
  function stage(name) {
    $("pick-stage").classList.toggle("hidden", name !== "pick");
    $("progress-stage").classList.toggle("hidden", name !== "progress");
    $("result-stage").classList.toggle("hidden", name !== "result");
    clearError();
  }

  // ---------- Kuyruk ----------
  function enqueueFiles(fileList) {
    if (!fileList || !fileList.length) return;
    clearError();
    const items = Array.from(fileList).map((f) => ({ file: f, status: "waiting" }));
    queue = queue.concat(items);
    cancelled = false;
    stage("progress");
    renderQueue();
    pump();
  }

  // Dosyaları MAX_CONCURRENT'ı aşmayacak şekilde başlat.
  function pump() {
    if (cancelled) return;
    while (activeCount < MAX_CONCURRENT) {
      const item = queue.find((q) => q.status === "waiting");
      if (!item) break;
      item.status = "uploading";
      activeCount++;
      uploadOne(item);
    }
    renderQueue();
    // hepsi bitti mi?
    if (activeCount === 0) {
      const anyDone = queue.some((q) => q.status === "done");
      const allTerminal = queue.every((q) => q.status === "done" || q.status === "failed");
      if (queue.length && allTerminal) {
        if (anyDone) showResult();
        else { stage("pick"); showError("Hiçbir dosya yüklenemedi."); queue = []; }
      }
    }
  }

  function itemDone(item) {
    activeCount = Math.max(0, activeCount - 1);
    renderQueue();
    pump();
  }

  // ---------- Tek dosya upload akışı ----------
  function uploadOne(item) {
    const f = item.file;
    // Sunucu temizlenmiş adı döndürür; link/görüntü bu adla kurulur (ham ad 404 yapar).
    item.safeName = f.name;
    const startBody = {
      name: f.name,
      size: f.size,
      mime: f.type || "application/octet-stream",
      expires_in_hours: currentExpiry(),
    };

    fetchWithRetry("/api/upload/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(startBody),
    })
      .then((r) => r.json().then((d) => ({ ok: r.ok, d })))
      .then(({ ok, d }) => {
        if (!ok) throw new Error(d.detail || "Upload başlatılamadı");
        item.token = d.token;
        item.partCount = d.part_count;
        item.segmentBytes = d.segment_bytes;
        if (d.name) item.safeName = d.name;
        renderQueue();
        return uploadParts(item);
      })
      .then(() => finishUpload(item))
      .catch((e) => {
        item.status = "failed";
        item.error = e.message;
        renderQueue();
        itemDone(item);
      });
  }

  // Parça yükleme (sıralı XHR) — item'a özel.
  function uploadParts(item) {
    return new Promise((resolve, reject) => {
      const f = item.file;
      let idx = 0;
      const next = () => {
        if (cancelled) {
          reject(new Error("iptal edildi"));
          return;
        }
        if (idx >= item.partCount) {
          setP1(100);
          resolve();
          return;
        }
        const start = idx * item.segmentBytes;
        const end = Math.min(start + item.segmentBytes, f.size);
        const blob = f.slice(start, end);
        const xhr = new XMLHttpRequest();
        item.xhr = xhr;
        xhr.open("POST", `/api/upload/${item.token}/parts/${idx}`);
        xhr.setRequestHeader("Content-Type", "application/octet-stream");
        xhr.onload = () => {
          if (xhr.status !== 200) {
            reject(new Error(`Parça ${idx + 1} yüklenemedi (${xhr.status})`));
            return;
          }
          idx++;
          next();
        };
        xhr.onerror = () => reject(new Error("Ağ hatası: sunucuya ulaşılamadı."));
        xhr.upload.onprogress = (e) => {
          if (e.lengthComputable) {
            const partPct = e.loaded / e.total;
            const overall = ((idx + partPct) / item.partCount) * 100;
            setP1(overall);
          }
        };
        xhr.send(blob);
      };
      next();
    });
  }

  function setP1(pct) {
    $("p1-fill").style.width = pct + "%";
    $("p1-pct").textContent = Math.round(pct) + "%";
  }
  function setP2(pct) {
    $("p2-fill").style.width = pct + "%";
    $("p2-pct").textContent = pct < 0 ? "—" : Math.round(pct) + "%";
  }

  // Seçili süreyi saat cinsinden döndürür (0 = süresiz)
  function currentExpiry() {
    const el = $("expiry-select");
    if (!el) return 0;
    return parseInt(el.value, 10) || 0;
  }

  // ---------- Bitiş + Telegram polling ----------
  function finishUpload(item) {
    $("p1-label").textContent = "Sunucuya gönderildi ✓";
    $("p2-label").textContent = "Buluta dağıtılıyor...";
    return fetchWithRetry(`/api/upload/${item.token}/finish`, { method: "POST" })
      .then((r) => r.json().then((d) => ({ ok: r.ok, d })))
      .then(({ ok, d }) => {
        if (d.status !== "started" && !ok) {
          throw new Error(d.detail || "Dağıtım başlatılamadı");
        }
        return pollStatus(item);
      });
  }

  function pollStatus(item) {
    return new Promise((resolve, reject) => {
      const tick = () => {
        fetch(`/api/upload/${item.token}/status`)
          .then((r) => r.json())
          .then((d) => {
            if (cancelled) {
              reject(new Error("iptal edildi"));
              return;
            }
            if (d.status === "ready") {
              setP2(100);
              $("p2-label").textContent = "Buluta dağıtıldı ✓";
              item.status = "done";
              item.link = `${location.origin}/api/download/${item.token}/${encodeURIComponent(item.safeName)}`;
              renderQueue();
              itemDone(item);
              resolve();
              return;
            }
            if (d.status === "failed") {
              reject(new Error(d.error || "bilinmeyen hata"));
              return;
            }
            const pct = d.part_count > 0 ? (d.done_parts / d.part_count) * 100 : 0;
            setP2(pct);
            setTimeout(tick, 1000);
          })
          .catch((e) => {
            if (cancelled) return;
            setTimeout(tick, 1500);
          });
      };
      tick();
    });
  }

  // ---------- 429 retry: bulk büyük batch'lerde upload/start rate-limit'e takılır ----------
  function fetchWithRetry(url, opts, attempts) {
    attempts = attempts || 0;
    return fetch(url, opts).then((r) => {
      if (r.status === 429 && attempts < 5) {
        const wait = 2000 + attempts * 2000; // 2s,4s,6s,8s,10s
        return new Promise((res) => setTimeout(res, wait)).then(() =>
          fetchWithRetry(url, opts, attempts + 1)
        );
      }
      return r;
    });
  }

  function showResult() {
    stage("result");
    const done = queue.filter((q) => q.status === "done");
    $("res-count").textContent =
      done.length + (done.length === 1 ? " dosya hazır" : " dosya hazır");
    const list = $("res-links");
    list.innerHTML = "";
    done.forEach((item, i) => {
      const row = document.createElement("div");
      row.className = "res-row";
      const nameSpan = document.createElement("span");
      nameSpan.className = "res-row-name";
      nameSpan.textContent = item.safeName;
      nameSpan.title = item.safeName;
      const input = document.createElement("input");
      input.type = "text";
      input.value = item.link;
      input.readOnly = true;
      const btn = document.createElement("button");
      btn.className = "btn btn-ghost";
      btn.textContent = "Kopyala";
      btn.addEventListener("click", () => copyText(item.link));
      row.appendChild(nameSpan);
      row.appendChild(input);
      row.appendChild(btn);
      list.appendChild(row);
    });
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast("Bağlantı kopyalandı!");
    } catch (e) {
      const el = document.createElement("textarea");
      el.value = text;
      document.body.appendChild(el);
      el.select();
      document.execCommand("copy");
      el.remove();
      toast("Bağlantı kopyalandı!");
    }
  }

  // ---------- Ctrl+V ile panodan yapıştırma ----------
  document.addEventListener("paste", (e) => {
    const items = (e.clipboardData && e.clipboardData.items) || [];
    const files = [];
    for (const item of items) {
      if (item.kind === "file") {
        const file = item.getAsFile();
        if (file) {
          e.preventDefault();
          // Ekran görüntüsü/panodan resim: isim yoksa üret
          if (!file.name || file.name === "image.png") {
            const ext = (file.type === "image/png" ? "png" : file.type.split("/")[1] || "bin");
            files.push(new File([file], `Yapistirilan-${Date.now()}.${ext}`, { type: file.type }));
          } else {
            files.push(file);
          }
        }
      }
    }
    if (files.length) enqueueFiles(files);
  });

  // ---------- URL ile upload (tek öğeli kuyruk) ----------
  $("url-btn").addEventListener("click", () => {
    const url = $("url-input").value.trim();
    if (!url) return;
    urlUpload(url);
  });
  $("url-input").addEventListener("keydown", (e) => {
    if (e.key === "Enter") $("url-btn").click();
  });

  function urlUpload(url) {
    clearError();
    stage("progress");
    $("up-name").textContent = url.length > 60 ? url.slice(0, 57) + "..." : url;
    $("up-size").textContent = "URL'den indiriliyor...";
    setP1(0);
    setP2(-1);
    $("p1-label").textContent = "Sunucu indiriyor";
    $("p2-label").textContent = "Buluta dağıtılıyor...";

    const item = { file: { name: url }, status: "uploading", safeName: url };
    queue = [item];
    activeCount = 1;
    cancelled = false;

    fetch("/api/upload/by-url", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url, expires_in_hours: currentExpiry() }),
    })
      .then((r) => r.json().then((d) => ({ ok: r.ok, d })))
      .then(({ ok, d }) => {
        if (!ok) throw new Error(d.detail || "URL indirilemedi");
        setP1(100);
        $("p1-label").textContent = "Sunucu indirdi ✓";
        item.token = d.token;
        if (d.name) item.safeName = d.name;
        return pollStatus(item);
      })
      .catch((e) => {
        item.status = "failed";
        item.error = e.message;
        renderQueue();
        stage("pick");
        showError(e.message);
        queue = [];
        activeCount = 0;
      });
  }

  // ---------- Olaylar ----------
  dropzone.addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", (e) => {
    enqueueFiles(e.target.files);
    e.target.value = ""; // aynı dosya tekrar seçilebilsin
  });

  ["dragover", "dragenter"].forEach((ev) =>
    dropzone.addEventListener(ev, (e) => {
      e.preventDefault();
      dropzone.classList.add("drag");
    })
  );
  ["dragleave", "dragend", "drop"].forEach((ev) =>
    dropzone.addEventListener(ev, (e) => {
      e.preventDefault();
      dropzone.classList.remove("drag");
    })
  );
  dropzone.addEventListener("drop", (e) => {
    if (e.dataTransfer.files.length) enqueueFiles(e.dataTransfer.files);
  });

  $("cancel-btn").addEventListener("click", () => {
    cancelled = true;
    queue.forEach((item) => {
      if (item.status === "uploading" && item.token) {
        if (item.xhr) item.xhr.abort();
        fetch(`/api/upload/${item.token}`, { method: "DELETE" }).catch(() => {});
      }
    });
    queue = [];
    activeCount = 0;
    stage("pick");
  });

  $("copy-all-btn").addEventListener("click", () => {
    const links = queue.filter((q) => q.status === "done").map((q) => q.link);
    if (links.length) copyText(links.join("\n"));
  });

  $("again-btn").addEventListener("click", () => {
    queue = [];
    activeCount = 0;
    cancelled = false;
    stage("pick");
  });

  // ---------- Kuyruk listesi render ----------
  function renderQueue() {
    const box = $("queue-box");
    if (!box) return;
    const doneCount = queue.filter((q) => q.status === "done").length;
    const total = queue.length;
    box.innerHTML = "";
    const header = document.createElement("div");
    header.className = "queue-header";
    header.textContent = `${doneCount}/${total} dosya yüklendi`;
    box.appendChild(header);
    queue.forEach((item) => {
      const row = document.createElement("div");
      row.className = "queue-row";
      const name = document.createElement("span");
      name.className = "queue-row-name";
      name.textContent = item.safeName || item.file.name;
      name.title = item.file.name;
      const st = document.createElement("span");
      st.className = "queue-row-status";
      switch (item.status) {
        case "waiting": st.textContent = "bekliyor"; st.classList.add("q-wait"); break;
        case "uploading": st.textContent = "yükleniyor"; st.classList.add("q-upload"); break;
        case "done": st.textContent = "✓"; st.classList.add("q-done"); break;
        case "failed": st.textContent = item.error ? "✗ " + item.error : "✗"; st.classList.add("q-fail"); break;
      }
      row.appendChild(name);
      row.appendChild(st);
      box.appendChild(row);
    });
  }

  let toastTimer = null;
  function toast(msg) {
    let t = document.querySelector(".toast");
    if (!t) {
      t = document.createElement("div");
      t.className = "toast";
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove("show"), 2000);
  }
})();
