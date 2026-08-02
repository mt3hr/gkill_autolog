// gkill autolog の Service Worker。
//
// 役割は2つ。
//   1. いま見えているタブ（フォーカスされたウィンドウのアクティブタブ）の
//      閲覧区間を browser_view イベントとして記録する。
//   2. content_media.js から届く再生実績を media_play イベントとして記録する。
//
// MV3 の Service Worker はいつでも停止されるため、状態とキューは
// すべて chrome.storage へ置き、メモリ上には持たない。

const ALARM_NAME = "gkill-autolog-tick";

// TICK_MINUTES は chrome.alarms の最小周期。これより短くはできない。
const TICK_MINUTES = 0.5;
const TICK_MS = TICK_MINUTES * 60 * 1000;

// STALE_MS を超えて心拍が途切れていたら、Service Worker が止まっていたとみなす。
// その場合は現在時刻ではなく最後の心拍時刻を閲覧終了時刻として使う。
const STALE_MS = TICK_MS * 3;

// QUEUE_LIMIT はキューに保持する上限。送信できない状態が続いても
// storage を食い潰さないように、古いものから捨てる。
const QUEUE_LIMIT = 5000;

const KEY_VIEW = "currentView";
const KEY_QUEUE = "queue";
const KEY_SETTINGS = "settings";
const KEY_PENDING_PLAYS = "pendingPlays";

// ---------------------------------------------------------------- 設定

async function loadSettings() {
  const stored = await chrome.storage.local.get(KEY_SETTINGS);
  const settings = stored[KEY_SETTINGS] || {};
  return {
    endpoint: settings.endpoint || "http://127.0.0.1:19921/ingest",
    token: settings.token || "",
  };
}

// ---------------------------------------------------------------- キュー

async function enqueue(event) {
  const stored = await chrome.storage.local.get(KEY_QUEUE);
  const queue = stored[KEY_QUEUE] || [];
  queue.push(event);
  if (queue.length > QUEUE_LIMIT) {
    queue.splice(0, queue.length - QUEUE_LIMIT);
  }
  await chrome.storage.local.set({ [KEY_QUEUE]: queue });
}

// flush はキューをまとめて送る。
// 送信できたものだけをキューから外すので、収集側が落ちていても失われない。
async function flush() {
  const { endpoint, token } = await loadSettings();
  if (!token) {
    return;
  }

  const stored = await chrome.storage.local.get(KEY_QUEUE);
  const queue = stored[KEY_QUEUE] || [];
  if (queue.length === 0) {
    return;
  }

  let response;
  try {
    response = await fetch(endpoint, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({ events: queue }),
    });
  } catch (error) {
    // 収集プログラムが動いていないだけ。次の心拍で再試行する。
    console.debug("gkill autolog: 送信できなかった", error);
    return;
  }

  if (!response.ok) {
    console.warn("gkill autolog: 送信に失敗した", response.status, await response.text());
    return;
  }

  // 送信中に積まれた分を消さないよう、送った件数だけを先頭から外す。
  const after = await chrome.storage.local.get(KEY_QUEUE);
  const current = after[KEY_QUEUE] || [];
  await chrome.storage.local.set({ [KEY_QUEUE]: current.slice(queue.length) });
}

// ---------------------------------------------------------------- 閲覧区間

// openView は新しい閲覧区間を開始する。
async function openView(tab, now) {
  if (!tab || !tab.url) {
    return;
  }
  await chrome.storage.local.set({
    [KEY_VIEW]: {
      url: tab.url,
      title: tab.title || "",
      tabId: tab.id,
      windowId: tab.windowId,
      startedAt: now,
      heartbeatAt: now,
    },
  });
}

// closeView は開いている閲覧区間を閉じ、browser_view イベントとして積む。
//
// 表示していた URL とタイトルはそのまま使う。要約も整形もしない。
// 30 秒未満の除外や不要ページの判定は normalize と Claude が行うため、ここでは絞らない。
async function closeView(now) {
  const stored = await chrome.storage.local.get(KEY_VIEW);
  const view = stored[KEY_VIEW];
  if (!view) {
    return;
  }
  await chrome.storage.local.remove(KEY_VIEW);

  // Service Worker が止まっていた場合、現在時刻は終了時刻として信用できない。
  const endedAt = now - view.heartbeatAt > STALE_MS ? view.heartbeatAt : now;
  if (endedAt <= view.startedAt) {
    return;
  }

  await enqueue({
    schema_version: 1,
    event_id: crypto.randomUUID(),
    event_type: "browser_view",
    start_time: new Date(view.startedAt).toISOString(),
    end_time: new Date(endedAt).toISOString(),
    captured_at: new Date(now).toISOString(),
    payload: {
      url: view.url,
      title: view.title,
      tab_id: view.tabId,
      window_id: view.windowId,
      // 見えているタブだけを開始条件にしているため、区間中は前面だったとみなせる。
      browser_focused: true,
      source: "chrome_extension",
    },
  });
}

// visibleTab はいま見えているタブを返す。
// ブラウザ自体が前面でなければ null を返し、閲覧区間は開かない。
async function visibleTab() {
  let window;
  try {
    window = await chrome.windows.getLastFocused({ populate: false });
  } catch {
    return null;
  }
  if (!window || !window.focused) {
    return null;
  }
  const tabs = await chrome.tabs.query({ active: true, windowId: window.id });
  return tabs.length > 0 ? tabs[0] : null;
}

// syncView は現在見えているタブに合わせて閲覧区間を開き直す。
// 同じ URL を見続けている間は開いたままにする。
async function syncView() {
  const now = Date.now();
  const tab = await visibleTab();

  const stored = await chrome.storage.local.get(KEY_VIEW);
  const view = stored[KEY_VIEW];

  if (!tab) {
    // 別アプリへ移った。閲覧終了。
    await closeView(now);
    return;
  }

  if (view && view.tabId === tab.id && view.url === tab.url) {
    // 同じページを見続けている。タイトルは後から確定することがあるので拾い直す。
    await chrome.storage.local.set({
      [KEY_VIEW]: { ...view, title: tab.title || view.title, heartbeatAt: now },
    });
    return;
  }

  // 同一URLの再表示も別セッションとして扱う（要件 §7.1）。
  await closeView(now);
  await openView(tab, now);
}

// ---------------------------------------------------------------- イベント登録

chrome.runtime.onInstalled.addListener(() => {
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: TICK_MINUTES });
  syncView();
});

chrome.runtime.onStartup.addListener(() => {
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: TICK_MINUTES });
  syncView();
});

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name !== ALARM_NAME) {
    return;
  }
  await syncView();
  await finalizePlays(Date.now());
  await flush();
});

chrome.tabs.onActivated.addListener(() => {
  syncView();
});

chrome.tabs.onUpdated.addListener((tabId, changeInfo) => {
  // URL の遷移とタイトルの確定の両方を拾う。
  if (changeInfo.url || changeInfo.title) {
    syncView();
  }
});

chrome.tabs.onRemoved.addListener(async (tabId) => {
  const stored = await chrome.storage.local.get(KEY_VIEW);
  if (stored[KEY_VIEW] && stored[KEY_VIEW].tabId === tabId) {
    await closeView(Date.now());
  }
});

chrome.windows.onFocusChanged.addListener(() => {
  syncView();
});

// ---------------------------------------------------------------- 再生実績

// content_media.js は再生中 10 秒ごとに途中経過を送ってくる。
// タブが突然閉じられて最後の報告が届かなくても、直前の報告までは残る。
//
// 報告は playId ごとに上書きし、更新が途切れたものを心拍のたびに確定させる。
// 1回の再生につき1件の media_play になる（要件 §8.1）。
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (!message || message.type !== "media_progress") {
    return false;
  }
  recordProgress(message).then(() => sendResponse({ received: true }));
  return true;
});

async function recordProgress(message) {
  if (!message.playId) {
    return;
  }

  // URL もタイトルも無い再生は、何を再生したのか分からないので記録しない。
  //
  // URL が無くタイトルだけの再生は残す。YouTube Music はアルバムアートから
  // 動画IDを取れないことがあり、そのとき URL は確定できないが、聴いていた事実は
  // 残せる。区間は常に TimeIs にする（要件 §8.1）。
  // 埋め合わせに検索URLや推測したURLを作ってはならない（要件 §8.2）。
  //
  // 動画IDは YouTube 系でしか取れないので条件にしない。
  if (!message.url && !message.title) {
    return;
  }

  const stored = await chrome.storage.local.get(KEY_PENDING_PLAYS);
  const pending = stored[KEY_PENDING_PLAYS] || {};
  const previous = pending[message.playId];

  pending[message.playId] = {
    service: message.service,
    url: message.url || "",
    videoId: message.videoId,
    // 確定済みのタイトルを空で潰さない。
    // MediaSession がまだ返さない間の報告が後から届くことがある。
    title: message.title || (previous ? previous.title : "") || "",
    artist: message.artist || (previous ? previous.artist : "") || "",
    playedSeconds: message.playedSeconds,
    startedAt: message.startedAt,
    endedAt: message.endedAt,
    updatedAt: Date.now(),
    // 再生が終わったことを content script が知っている場合は即座に確定させる。
    finished: Boolean(message.finished),
  };

  await chrome.storage.local.set({ [KEY_PENDING_PLAYS]: pending });
  await finalizePlays(Date.now());
}

// finalizePlays は報告が途切れた再生を media_play イベントとして確定させる。
async function finalizePlays(now) {
  const stored = await chrome.storage.local.get(KEY_PENDING_PLAYS);
  const pending = stored[KEY_PENDING_PLAYS] || {};

  const remaining = {};
  for (const [playId, play] of Object.entries(pending)) {
    if (!play.finished && now - play.updatedAt <= STALE_MS) {
      remaining[playId] = play;
      continue;
    }
    if (play.playedSeconds <= 0) {
      continue;
    }
    await enqueue({
      schema_version: 1,
      event_id: crypto.randomUUID(),
      event_type: "media_play",
      start_time: new Date(play.startedAt).toISOString(),
      end_time: new Date(play.endedAt).toISOString(),
      captured_at: new Date(now).toISOString(),
      payload: {
        service: play.service,
        // URL を確定できなかった再生は項目ごと落とす。normalize は
        // URL 無しの再生を TimeIs だけにする（要件 §8.1）。
        url: play.url || undefined,
        // 動画IDは YouTube 系でしか取れない。無いときは項目ごと落とす。
        video_id: play.videoId || undefined,
        title: play.title,
        artist: play.artist,
        // 一時停止時間と広告再生時間を含めない実再生秒数。
        // 30 秒未満を切る判定は normalize 側で行う。
        played_seconds: play.playedSeconds,
      },
    });
  }

  await chrome.storage.local.set({ [KEY_PENDING_PLAYS]: remaining });
}
