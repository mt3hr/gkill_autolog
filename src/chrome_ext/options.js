const KEY_SETTINGS = "settings";
const KEY_QUEUE = "queue";

const endpointInput = document.getElementById("endpoint");
const tokenInput = document.getElementById("token");
const statusLabel = document.getElementById("status");
const queueCount = document.getElementById("queue-count");

async function load() {
  const stored = await chrome.storage.local.get([KEY_SETTINGS, KEY_QUEUE]);
  const settings = stored[KEY_SETTINGS] || {};
  endpointInput.value = settings.endpoint || "http://127.0.0.1:19921/ingest";
  tokenInput.value = settings.token || "";
  queueCount.textContent = String((stored[KEY_QUEUE] || []).length);
}

document.getElementById("save").addEventListener("click", async () => {
  const endpoint = endpointInput.value.trim();
  const token = tokenInput.value.trim();

  if (!token) {
    statusLabel.textContent = "共有トークンを入力してください";
    return;
  }

  await chrome.storage.local.set({ [KEY_SETTINGS]: { endpoint, token } });
  statusLabel.textContent = "保存しました";
  setTimeout(() => {
    statusLabel.textContent = "";
  }, 2000);
});

load();
