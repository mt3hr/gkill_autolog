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

test("同じ確定が2回積まれても event_id が同じで、受け口の重複排除に畳まれる形になる", async () => {
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
  // 確定済みの pending は消えているので、同じ報告の再送は改めて確定される。
  await sendMediaProgress(report);

  const queue = store.queue || [];
  const ids = queue.filter((event) => event.event_type === "media_play").map((event) => event.event_id);
  assert.equal(ids.length, 2);
  assert.equal(ids[0], ids[1], "同じ確定から別の event_id ができた");
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
