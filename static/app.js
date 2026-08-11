/* Hyaena Storage — upload sayfası mantığı */
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const dropzone = $("dropzone");
  const fileInput = $("file-input");
  let current = null; // { file, token, partCount, segmentBytes, xhr }

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

  // ---------- Dosya seçimi ----------
  function pickFile(file) {
    if (!file) return;
    if (current && current.status === "uploading") return;
    clearError();
    current = { file };

    // start: token + segment bilgisi al
    fetch("/api/upload/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: file.name,
        size: file.size,
        mime: file.type || "application/octet-stream",
        expires_in_hours: currentExpiry(),
      }),
    })
      .then((r) => r.json().then((d) => ({ ok: r.ok, d })))
      .then(({ ok, d }) => {
        if (!ok) throw new Error(d.detail || "Upload başlatılamadı");
        current.token = d.token;
        current.partCount = d.part_count;
        current.segmentBytes = d.segment_bytes;
        stage("progress");
        $("up-name").textContent = file.name;
        $("up-size").textContent = fmt(file.size);
        uploadParts();
      })
      .catch((e) => {
        stage("pick");
        showError(e.message);
        current = null;
      });
  }

  // ---------- Parça yükleme (sıralı XHR) ----------
  function uploadParts() {
    const f = current.file;
    let idx = 0;

    const next = () => {
      if (idx >= current.partCount) {
        setP1(100);
        finishUpload();
        return;
      }
      const start = idx * current.segmentBytes;
      const end = Math.min(start + current.segmentBytes, f.size);
      const blob = f.slice(start, end);
      const xhr = new XMLHttpRequest();
      current.xhr = xhr;

      xhr.open("POST", `/api/upload/${current.token}/parts/${idx}`);
      xhr.setRequestHeader("Content-Type", "application/octet-stream");
      xhr.onload = () => {
        if (xhr.status !== 200) {
          showError(`Parça ${idx + 1} yüklenemedi (${xhr.status})`);
          current.status = "error";
          return;
        }
        idx++;
        next();
      };
      xhr.onerror = () => {
        showError("Ağ hatası: sunucuya ulaşılamadı.");
        current.status = "error";
      };
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) {
          const partPct = e.loaded / e.total;
          const overall = ((idx + partPct) / current.partCount) * 100;
          setP1(overall);
        }
      };
      xhr.send(blob);
    };
    next();
  }

  function setP1(pct) {
    $("p1-fill").style.width = pct + "%";
    $("p1-pct").textContent = Math.round(pct) + "%";
  }

  // Seçili süreyi saat cinsinden döndürür (0 = süresiz)
  function currentExpiry() {
    const el = $("expiry-select");
    if (!el) return 0;
    return parseInt(el.value, 10) || 0;
  }
  function setP2(pct) {
    $("p2-fill").style.width = pct + "%";
    $("p2-pct").textContent = pct < 0 ? "—" : Math.round(pct) + "%";
  }

  // ---------- Bitiş + Telegram polling ----------
  function finishUpload() {
    $("p1-label").textContent = "Sunucuya gönderildi ✓";
    $("p2-label").textContent = "Buluta dağıtılıyor...";

    fetch(`/api/upload/${current.token}/finish`, { method: "POST" })
      .then((r) => r.json())
      .then((d) => {
        if (d.status !== "started" && !r.ok) {
          throw new Error(d.detail || "Dağıtım başlatılamadı");
        }
        pollStatus();
      })
      .catch((e) => {
        showError(e.message);
        current.status = "error";
      });
  }

  function pollStatus() {
    fetch(`/api/upload/${current.token}/status`)
      .then((r) => r.json())
      .then((d) => {
        if (d.status === "ready") {
          setP2(100);
          $("p2-label").textContent = "Buluta dağıtıldı ✓";
          if (!current.file.size && d.received_bytes) current.file.size = d.received_bytes;
          showResult();
          return;
        }
        if (d.status === "failed") {
          showError("Dağıtım başarısız: " + (d.error || "bilinmeyen hata"));
          current.status = "error";
          return;
        }
        const pct = d.part_count > 0 ? (d.done_parts / d.part_count) * 100 : 0;
        setP2(pct);
        setTimeout(pollStatus, 1000);
      })
      .catch((e) => {
        showError("Durum alınamadı: " + e.message);
        setTimeout(pollStatus, 1500);
      });
  }

  function showResult() {
    stage("result");
    $("res-name").textContent = current.file.name;
    $("res-size").textContent = current.file.size ? fmt(current.file.size) : "";
    // süre rozeti: süreli dosyalarda "Kalıcı" yerine seçilen süre
    const badge = document.querySelector(".badge");
    if (badge) {
      const exp = currentExpiry();
      badge.textContent = exp ? expiryLabel(exp) : "Kalıcı";
    }
    const link = `${location.origin}/api/download/${current.token}/${encodeURIComponent(current.file.name)}`;
    $("res-link").value = link;
  }

  // Saat değerini okunur etikete çevirir
  function expiryLabel(hours) {
    const map = { 1: "1 Saat", 24: "1 Gün", 168: "1 Hafta", 720: "1 Ay" };
    return map[hours] || "Kalıcı";
  }

  // ---------- Ctrl+V ile panodan yapıştırma ----------
  document.addEventListener("paste", (e) => {
    const items = (e.clipboardData && e.clipboardData.items) || [];
    for (const item of items) {
      if (item.kind === "file") {
        const file = item.getAsFile();
        if (file) {
          e.preventDefault();
          // Ekran görüntüsü/panodan resim: isim yoksa üret
          if (!file.name || file.name === "image.png") {
            const ext = (file.type === "image/png" ? "png" : file.type.split("/")[1] || "bin");
            const renamed = new File([file], `Yapistirilan-${Date.now()}.${ext}`, { type: file.type });
            pickFile(renamed);
          } else {
            pickFile(file);
          }
          return;
        }
      }
    }
  });

  // ---------- URL ile upload ----------
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
        // URL upload server-side; doğrudan status poll başlat
        current = { file: { name: d.name }, token: d.token };
        pollStatus();
      })
      .catch((e) => {
        stage("pick");
        showError(e.message);
        current = null;
      });
  }

  // ---------- Olaylar ----------
  dropzone.addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", (e) => pickFile(e.target.files[0]));

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
    if (e.dataTransfer.files.length) pickFile(e.dataTransfer.files[0]);
  });

  $("cancel-btn").addEventListener("click", () => {
    if (current && current.token) {
      if (current.xhr) current.xhr.abort();
      fetch(`/api/upload/${current.token}`, { method: "DELETE" }).catch(() => {});
    }
    current = null;
    stage("pick");
  });

  $("copy-btn").addEventListener("click", async () => {
    const v = $("res-link").value;
    try {
      await navigator.clipboard.writeText(v);
      toast("Bağlantı kopyalandı!");
    } catch (e) {
      $("res-link").select();
      document.execCommand("copy");
      toast("Bağlantı kopyalandı!");
    }
  });

  $("again-btn").addEventListener("click", () => {
    current = null;
    fileInput.value = "";
    stage("pick");
  });

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
