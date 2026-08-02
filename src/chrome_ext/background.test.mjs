// background.js (Service Worker) を Node 上で動かして検証する。
//
// ブラウザの完全な再現ではない。検証したい性質に必要な部分だけをスタブする。
//   - chrome.storage の get/set が必ず非同期になる (await の切れ目で処理が交錯する)
//   - イベントリスナの登録と発火
//
// 実行: npm run test_chrome_ext

import test from "node:test";
import assert from "node:assert/strict";

const listeners = {
  installed: [],
  startup: [],
  message: [],
  alarm: [],
  activated: [],
  updated: [],
  removed: [],
  focus: [],
};

let store = {};
// null ならブラウザが前面でない状態。
let visibleTab = null;

globalThis.chrome = {
  storage: {
    local: {
      async get(keys) {
        // 実物と同じく必ず非同期にする。ここが同期だと交錯が再現できない。
        await Promise.resolve();
        const wanted = Array.isArray(keys) ? keys : [keys];
        const out = {};
        for (const key of wanted) {
          if (key in store) {
            out[key] = structuredClone(store[key]);
          }
        }
        return out;
      },
      async set(items) {
        await Promise.resolve();
        for (const [key, value] of Object.entries(items)) {
          store[key] = structuredClone(value);
        }
      },
      async remove(key) {
        await Promise.resolve();
        delete store[key];
      },
    },
  },
  alarms: {
    async create() {},
    async get() {
      return { name: "gkill-autolog-tick" };
    },
    onAlarm: { addListener: (fn) => listeners.alarm.push(fn) },
  },
  runtime: {
    onInstalled: { addListener: (fn) => listeners.installed.push(fn) },
    onStartup: { addListener: (fn) => listeners.startup.push(fn) },
    onMessage: { addListener: (fn) => listeners.message.push(fn) },
  },
  tabs: {
    onActivated: { addListener: (fn) => listeners.activated.push(fn) },
    onUpdated: { addListener: (fn) => listeners.updated.push(fn) },
    onRemoved: { addListener: (fn) => listeners.removed.push(fn) },
    async query() {
      return visibleTab ? [visibleTab] : [];
    },
  },
  windows: {
    onFocusChanged: { addListener: (fn) => listeners.focus.push(fn) },
    async getLastFocused() {
      return visibleTab ? { id: visibleTab.windowId, focused: true } : { focused: false };
    },
  },
};

await import("./background.js");

// sendMediaProgress は content script からの報告を再現する。応答が返るまで待つ。
function sendMediaProgress(message) {
  return new Promise((resolve) => {
    const handled = listeners.message[0]({ type: "media_progress", ...message }, {}, resolve);
    if (handled !== true) {
      resolve(undefined);
    }
  });
}

function fireTabRemoved(tabId) {
  return listeners.removed[0](tabId);
}

function fireAlarm() {
  return listeners.alarm[0]({ name: "gkill-autolog-tick" });
}

test("タブ閉鎖の閲覧確定と再生の最終報告が並行しても、イベントが両方残る", async () => {
  // 再生中のタブを閉じると onRemoved (閲覧の確定) と pagehide の報告 (再生の確定) が
  // ほぼ同時に走る。storage の get→set が交錯すると後勝ちで片方が消える。
  store = {};
  const now = Date.now();
  store.currentView = {
    url: "https://example.com/article",
    title: "記事",
    tabId: 5,
    windowId: 1,
    startedAt: now - 60_000,
    heartbeatAt: now,
  };

  await Promise.all([
    fireTabRemoved(5),
    sendMediaProgress({
      playId: "p1",
      service: "web",
      url: "https://example.com/article",
      title: "記事",
      playedSeconds: 40,
      startedAt: now - 50_000,
      endedAt: now,
      finished: true,
    }),
  ]);

  const types = (store.queue || []).map((event) => event.event_type).sort();
  assert.deepEqual(types, ["browser_view", "media_play"],
    "並行実行でイベントが失われた");
});

test("複数の再生報告が並行しても、pendingPlays が後勝ちで消えない", async () => {
  store = {};
  const now = Date.now();

  // 2つのタブの再生報告が同時に届く。
  await Promise.all([
    sendMediaProgress({
      playId: "pa",
      service: "web",
      url: "https://example.com/a",
      title: "A",
      playedSeconds: 5,
      startedAt: now - 10_000,
      endedAt: now,
      finished: false,
    }),
    sendMediaProgress({
      playId: "pb",
      service: "web",
      url: "https://example.com/b",
      title: "B",
      playedSeconds: 6,
      startedAt: now - 10_000,
      endedAt: now,
      finished: false,
    }),
  ]);

  const pending = store.pendingPlays || {};
  assert.deepEqual(Object.keys(pending).sort(), ["pa", "pb"],
    "並行した報告の片方が消えた");
});

test("確定済みと同じ内容の再送は、新しいイベントを作らない", async () => {
  store = {};
  const now = Date.now();
  const report = {
    playId: "p-dup",
    service: "web",
    url: "https://example.com/v",
    title: "V",
    playedSeconds: 45,
    startedAt: now - 60_000,
    endedAt: now - 1_000,
    finished: true,
  };

  await sendMediaProgress(report);
  // 墓標 (finalizedPlays) が累計値を覚えているので、進んでいない再送は捨てられる。
  await sendMediaProgress(report);

  const plays = (store.queue || []).filter((event) => event.event_type === "media_play");
  assert.equal(plays.length, 1, "確定済みの再送から新しいイベントができた");
});

test("確定後に同じ再生が続いた場合、続きは差分の区間になり二重計上しない", async () => {
  // スリープ復帰の典型: 報告が途絶えて心拍が確定させたあと、
  // ページ側は生きていて累計の playedSeconds のまま報告を再開する。
  store = {};
  const now = Date.now();
  const firstEnd = now - 200_000;
  store.pendingPlays = {
    resumed: {
      service: "web",
      url: "https://example.com/long",
      videoId: "",
      title: "長い動画",
      artist: "",
      playedSeconds: 50,
      startedAt: now - 300_000,
      endedAt: firstEnd,
      updatedAt: now - 120_000, // STALE_MS (90秒) より古い
      finished: false,
    },
  };

  // 心拍が途絶えた再生を確定させる (1本目: 50秒)。
  await fireAlarm();

  // ページが累計 80 秒で報告を再開して終わる。
  await sendMediaProgress({
    playId: "resumed",
    service: "web",
    url: "https://example.com/long",
    title: "長い動画",
    playedSeconds: 80,
    startedAt: now - 300_000,
    endedAt: now - 1_000,
    finished: true,
  });

  const plays = (store.queue || []).filter((event) => event.event_type === "media_play");
  assert.equal(plays.length, 2, "確定と続きで2本になるはず");
  assert.equal(plays[0].payload.played_seconds, 50);
  assert.equal(plays[1].payload.played_seconds, 30, "続きは差分 (80-50) だけを数える");
  assert.equal(plays[1].start_time, new Date(firstEnd).toISOString(),
    "続きの区間は前回の終了から始まる");
  assert.notEqual(plays[0].event_id, plays[1].event_id);

  const total = plays.reduce((sum, event) => sum + event.payload.played_seconds, 0);
  assert.equal(total, 80, "合計が実再生時間と一致する (二重計上しない)");
});

test("アラームの心拍で報告が途切れた再生が確定する", async () => {
  store = {};
  const now = Date.now();
  // STALE_MS (90秒) より古い報告を仕込む。
  store.pendingPlays = {
    stale: {
      service: "web",
      url: "https://example.com/old",
      videoId: "",
      title: "古い再生",
      artist: "",
      playedSeconds: 50,
      startedAt: now - 300_000,
      endedAt: now - 200_000,
      updatedAt: now - 120_000,
      finished: false,
    },
  };

  await fireAlarm();

  const queue = store.queue || [];
  const plays = queue.filter((event) => event.event_type === "media_play");
  assert.equal(plays.length, 1, "途絶えた再生が確定されていない");
  assert.equal(store.pendingPlays.stale, undefined, "確定した再生が pending に残っている");
});

test("心拍が長く途絶えたあとの同じタブは、眠っていた時間を閲覧に含めず開き直す", async () => {
  // スリープ復帰後、ロックなし運用ではフォーカスも同じタブのまま心拍が再開する。
  // 継続扱いにすると眠っていた数時間が「閲覧中」になる。
  store = {};
  const now = Date.now();
  const lastBeat = now - 3 * 60 * 60 * 1000; // 3時間前に心拍が止まった
  store.currentView = {
    url: "https://example.com/doc",
    title: "資料",
    tabId: 7,
    windowId: 1,
    startedAt: lastBeat - 120_000,
    heartbeatAt: lastBeat,
  };
  visibleTab = { id: 7, windowId: 1, url: "https://example.com/doc", title: "資料" };

  await fireAlarm();
  visibleTab = null;

  const views = (store.queue || []).filter((event) => event.event_type === "browser_view");
  assert.equal(views.length, 1, "眠る前の区間が閉じられていない");
  assert.equal(views[0].end_time, new Date(lastBeat).toISOString(),
    "終了時刻が最後の心拍になっていない (眠っていた時間が閲覧に含まれた)");
  assert.ok(store.currentView, "復帰後の新しい区間が開かれていない");
  assert.ok(store.currentView.startedAt >= now, "新しい区間の開始が復帰後になっていない");
});

test("400 以外の 4xx (環境起因) ではキューを捨てない", async () => {
  // 送信先のパス違いなどで別のサーバが 404 を返す場合。
  // 二分破棄に入ると、設定を直す前にキューが空になってしまう。
  store = {};
  store.settings = { endpoint: "http://127.0.0.1:19921/ingest", token: "t" };
  store.queue = [{ event_id: "x1", event_type: "browser_view" }];
  globalThis.fetch = async () => ({ ok: false, status: 404, text: async () => "not found" });

  await fireAlarm();

  assert.equal((store.queue || []).length, 1, "環境起因の 4xx でイベントが捨てられた");
});

test("受け口の 400 は原因の1件だけを捨てて、残りは送って前へ進む", async () => {
  store = {};
  store.settings = { endpoint: "http://127.0.0.1:19921/ingest", token: "t" };
  store.queue = [
    { event_id: "bad", event_type: "browser_view" },
    { event_id: "good", event_type: "browser_view" },
  ];
  const sentBatches = [];
  globalThis.fetch = async (_url, init) => {
    const events = JSON.parse(init.body).events;
    sentBatches.push(events.map((event) => event.event_id));
    if (events.some((event) => event.event_id === "bad")) {
      return { ok: false, status: 400, text: async () => "invalid event" };
    }
    return { ok: true, status: 200, text: async () => "" };
  };

  await fireAlarm();

  assert.equal((store.queue || []).length, 0,
    `不正な1件の特定と破棄で前へ進めていない (送信履歴: ${JSON.stringify(sentBatches)})`);
});
