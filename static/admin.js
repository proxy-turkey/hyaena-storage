/* Hyaena Storage — admin panel mantığı */
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  let filesPage = 0;
  const PER = 25;

  function fmt(bytes) {
    if (!bytes) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }
  function toast(msg) {
    let t = document.querySelector(".toast");
    if (!t) {
      t = document.createElement("div");
      t.className = "toast";
      document.body.appendChild(t);
    }
    t.textContent = msg;
    t.classList.add("show");
    setTimeout(() => t.classList.remove("show"), 2000);
  }

  async function api(path, opts = {}) {
    const r = await fetch(path, {
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      ...opts,
    });
    if (r.status === 401) {
      location.reload();
      throw new Error("yetkisiz");
    }
    if (!r.ok) {
      const d = await r.json().catch(() => ({}));
      throw new Error(d.detail || `Hata ${r.status}`);
    }
    if (r.status === 204) return null;
    return r.json();
  }

  // ---------- Giriş kontrolü ----------
  async function init() {
    try {
      const s = await api("/api/admin/session");
      if (s.authenticated) {
        showPanel();
      } else {
        showLogin();
      }
    } catch (e) {
      showLogin();
    }
  }

  function showLogin() {
    $("login-view").classList.remove("hidden");
    $("panel-view").classList.add("hidden");
  }
  function showPanel() {
    $("login-view").classList.add("hidden");
    $("panel-view").classList.remove("hidden");
    loadDashboard();
  }

  $("login-btn").addEventListener("click", async () => {
    const pass = $("login-pass").value;
    $("login-error").classList.add("hidden");
    try {
      await api("/api/admin/login", { method: "POST", body: JSON.stringify({ password: pass }) });
      $("login-pass").value = "";
      showPanel();
    } catch (e) {
      $("login-error").textContent = e.message;
      $("login-error").classList.remove("hidden");
    }
  });
  $("login-pass").addEventListener("keydown", (e) => {
    if (e.key === "Enter") $("login-btn").click();
  });

  // ---------- Sekmeler ----------
  document.querySelectorAll(".admin-nav button").forEach((b) => {
    b.addEventListener("click", () => {
      const tab = b.dataset.tab;
      if (tab === "logout") {
        api("/api/admin/logout", { method: "POST" }).finally(() => location.reload());
        return;
      }
      document.querySelectorAll(".admin-nav button").forEach((x) => x.classList.remove("active"));
      b.classList.add("active");
      document.querySelectorAll("[id^='tab-']").forEach((x) => x.classList.add("hidden"));
      $(`tab-${tab}`).classList.remove("hidden");
      if (tab === "dashboard") loadDashboard();
      if (tab === "channels") loadChannels();
      if (tab === "files") loadFiles();
      if (tab === "settings") loadSettings();
    });
  });

  // ---------- Dashboard ----------
  async function loadDashboard() {
    try {
      const s = await api("/api/admin/summary");
      const totalGb = s.total_bytes / 1024 ** 3;
      $("stat-cards").innerHTML = `
        <div class="stat"><div class="num">${s.total_files}</div><div class="lbl">Toplam dosya</div></div>
        <div class="stat"><div class="num">${totalGb.toFixed(2)} GB</div><div class="lbl">Depolanan</div></div>
        <div class="stat"><div class="num">${s.today_files}</div><div class="lbl">Bugün yüklenen</div></div>
        <div class="stat"><div class="num">${s.channels_count}</div><div class="lbl">Kanal</div></div>
      `;
      // Kullanım: şu an bilinmeyen bir üst limit varsay — sadece GB göster
      $("usage-fill").style.width = Math.min(100, (s.total_bytes / 1024 ** 4) * 100) + "%";
      $("usage-text").textContent = `${fmt(s.total_bytes)} depolanıyor (segment ${fmt(s.segment_bytes)})`;
    } catch (e) {
      toast(e.message);
    }
  }

  // ---------- Kanallar ----------
  async function loadChannels() {
    try {
      const d = await api("/api/admin/channels");
      $("channel-hint").textContent =
        `Filo büyüyor: her gün otomatik +1 kanal eklenir. Şu an ${d.channels.length} kanal.`;
      $("channels-tbody").innerHTML = d.channels
        .map(
          (c) => `
          <tr>
            <td>${c.id}</td>
            <td><strong>${esc(c.title)}</strong></td>
            <td>${c.telegram_id}</td>
            <td class="muted">${esc(c.created_day)}</td>
          </tr>`
        )
        .join("");
    } catch (e) {
      toast(e.message);
    }
  }

  $("create-channel-btn").addEventListener("click", async () => {
    const btn = $("create-channel-btn");
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span>Oluşturuluyor...';
    try {
      const d = await api("/api/admin/channels/create-now", { method: "POST" });
      toast(d.created ? "Kanal oluşturuldu ✓" : "Kanal zaten mevcut veya oluşturulamadı");
      loadChannels();
    } catch (e) {
      toast("Hata: " + e.message);
    } finally {
      btn.disabled = false;
      btn.textContent = "+ Kanal oluştur";
    }
  });

  // ---------- Dosyalar ----------
  async function loadFiles() {
    try {
      const d = await api(`/api/admin/files?limit=${PER}&offset=${filesPage * PER}`);
      $("files-count").textContent = `Toplam ${d.total} dosya`;
      $("files-tbody").innerHTML = d.files
        .map((f) => {
          const stCls = f.status === "ready" ? "ok" : f.status === "failed" ? "bad" : "warn";
          return `
          <tr>
            <td>
              <strong>${esc(f.original_name)}</strong>
              <div class="muted">${esc(f.token)}</div>
            </td>
            <td>${fmt(f.size)}</td>
            <td class="muted">${esc((f.created_at || "").slice(0, 16))}</td>
            <td>${f.done_parts}/${f.part_count}</td>
            <td><span class="tag ${stCls}">${esc(f.status)}</span></td>
            <td>
              <button class="btn btn-ghost" onclick="window.open('${f.download_url}','_blank')">⬇</button>
              <button class="btn btn-danger" onclick="deleteFile('${f.token}')">🗑</button>
            </td>
          </tr>`;
        })
        .join("");
      $("files-prev").disabled = filesPage === 0;
    } catch (e) {
      toast(e.message);
    }
  }

  $("files-prev").addEventListener("click", () => {
    if (filesPage > 0) {
      filesPage--;
      loadFiles();
    }
  });
  $("files-next").addEventListener("click", () => {
    filesPage++;
    loadFiles();
  });

  window.deleteFile = async (token) => {
    if (!confirm("Bu dosya silinsin mi? Telegram mesajları da kalıcı olarak silinecek.")) return;
    try {
      const d = await api(`/api/admin/files/${token}`, { method: "DELETE" });
      toast(`${d.parts_deleted} parça silindi`);
      loadFiles();
      loadDashboard();
    } catch (e) {
      toast("Hata: " + e.message);
    }
  };

  // ---------- Ayarlar ----------
  async function loadSettings() {
    try {
      const s = await api("/api/admin/settings");
      $("settings-tbody").innerHTML = `
        <tr><td>Segment boyutu</td><td>${fmt(s.segment_bytes)}</td></tr>
        <tr><td>Maks. dosya boyutu</td><td>${fmt(s.max_upload_bytes)}</td></tr>
        <tr><td>Kanal oluşturma aralığı</td><td>${s.channel_interval_sn} sn</td></tr>
        <tr><td>Mesaj arası bekleme</td><td>${s.inter_message_sleep} sn</td></tr>
        <tr><td>Hız sınırı</td><td>${s.rate_limit_per_min} istek/dk/IP</td></tr>
      `;
    } catch (e) {
      toast(e.message);
    }
  }

  function esc(s) {
    const div = document.createElement("div");
    div.textContent = s == null ? "" : String(s);
    return div.innerHTML;
  }

  init();
})();
