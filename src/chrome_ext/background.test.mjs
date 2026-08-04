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

function fireTabActivated(tabId, windowId) {
  return listeners.activated[0]({ tabId, windowId });
}

function fireTabUpdated(tabId, changeInfo) {
  return listeners.updated[0](tabId, changeInfo, visibleTab);
}

function fireFocusChanged(windowId) {
  return listeners.focus[0](windowId);
}

// activated / updated / focus のリスナは withState の完了を返さない (投げっぱなし)。
// 状態を触る処理は直列化チェーンで1列に並ぶので、どのタブにも一致しない
// onRemoved を後ろに積んで待てば、先行する syncView の完了が保証される。
function settleListeners() {
  return fireTabRemoved(-1);
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

test("差分ゼロの再送が墓標を生かし続け、TTL 超えの再開でも累計を二重計上しない", async () => {
  // c37715b の回帰テスト。タブを開いたまま FINALIZED_TTL_MS (24時間) を超えて
  // 一時停止した再生が再開すると、「確定から24時間」で墓標を掃除する実装では
  // 墓標が消えていて、累計の全量が新しい再生として二重計上されていた。
  // 掃除の起点は「最後にその playId の報告を見てから」でなければならない。
  store = {};
  const realDateNow = Date.now;
  try {
    let now = realDateNow();
    Date.now = () => now;
    const playStartedAt = now - 200_000;
    const firstEnd = now - 1_000;

    // (a) 100 秒の再生を確定させ、墓標を作る。
    await sendMediaProgress({
      playId: "paused-long",
      service: "web",
      url: "https://example.com/movie",
      title: "映画",
      playedSeconds: 100,
      startedAt: playStartedAt,
      endedAt: firstEnd,
      finished: true,
    });

    // (b) 一時停止したまま、content script は累計 100 秒 (差分ゼロ) の報告を続ける。
    // 8時間おきの再送が墓標の finalizedAt を更新するので、間に心拍の掃除が
    // 走っても墓標は生き続ける。最後の再送時点で最初の確定から 32 時間 > TTL。
    for (const hours of [8, 16, 24, 32]) {
      now = firstEnd + 1_000 + hours * 60 * 60 * 1000;
      await fireAlarm();
      await sendMediaProgress({
        playId: "paused-long",
        service: "web",
        url: "https://example.com/movie",
        title: "映画",
        playedSeconds: 100,
        startedAt: playStartedAt,
        endedAt: firstEnd,
        finished: false,
      });
    }

    // (c) 最初の確定から FINALIZED_TTL_MS を超えたあと、再開して 30 秒進んで終わる。
    now += 60_000;
    await sendMediaProgress({
      playId: "paused-long",
      service: "web",
      url: "https://example.com/movie",
      title: "映画",
      playedSeconds: 130,
      startedAt: playStartedAt,
      endedAt: now - 1_000,
      finished: true,
    });

    const plays = (store.queue || []).filter((event) => event.event_type === "media_play");
    assert.equal(plays.length, 2, "最初の確定と再開の続きで2本になるはず");
    assert.equal(plays[1].payload.played_seconds, 30,
      "墓標が消えて累計の全量が新しい再生として二重計上された");
    assert.equal(plays[1].start_time, new Date(firstEnd).toISOString(),
      "続きの区間は前回の確定の終了から始まる");
    const total = plays.reduce((sum, event) => sum + event.payload.played_seconds, 0);
    assert.equal(total, 130, "合計が実再生時間と一致する (二重計上しない)");
  } finally {
    Date.now = realDateNow;
  }
});

test("タブ切替で前のタブの閲覧が確定し、新しいタブの閲覧が始まる", async () => {
  // onActivated は閲覧区間を切り替える主経路。前のタブの区間が閉じられないと
  // 閲覧が開きっぱなしになり、新しい区間が開かれないと次の閲覧が残らない。
  store = {};
  const realDateNow = Date.now;
  try {
    let now = realDateNow();
    Date.now = () => now;
    store.currentView = {
      url: "https://example.com/first",
      title: "前のタブ",
      tabId: 1,
      windowId: 1,
      startedAt: now - 60_000,
      heartbeatAt: now,
    };

    // 5 秒後に別のタブへ切り替える。
    now += 5_000;
    visibleTab = { id: 2, windowId: 1, url: "https://example.com/second", title: "次のタブ" };
    fireTabActivated(2, 1);
    await settleListeners();

    const views = (store.queue || []).filter((event) => event.event_type === "browser_view");
    assert.equal(views.length, 1, "前のタブの閲覧が確定されていない");
    assert.equal(views[0].payload.url, "https://example.com/first");
    assert.equal(views[0].payload.tab_id, 1);
    assert.equal(views[0].end_time, new Date(now).toISOString(),
      "前の区間が切り替えた時刻で閉じられていない");
    assert.ok(store.currentView, "新しいタブの閲覧が開かれていない");
    assert.equal(store.currentView.tabId, 2);
    assert.equal(store.currentView.url, "https://example.com/second");
    assert.equal(store.currentView.startedAt, now,
      "新しい区間の開始が切り替えた時刻になっていない");
  } finally {
    Date.now = realDateNow;
    visibleTab = null;
  }
});

test("同一 URL の再表示は、前の閲覧の続きではなく別の閲覧になる", async () => {
  // 要件 §7.1。同じタブで A→B→A と行き来したとき、A の2回目を1回目の続きに
  // すると間に挟まった B と区間が重なる。再表示は新しい startedAt で開き直され、
  // event_id も別になる (同じだと受け口の (device, event_id) で1件に畳まれて
  // 2回目の閲覧が消える) ことを検証する。
  store = {};
  const realDateNow = Date.now;
  try {
    let now = realDateNow();
    Date.now = () => now;
    const urlA = "https://example.com/a";
    const urlB = "https://example.com/b";

    visibleTab = { id: 9, windowId: 1, url: urlA, title: "A" };
    fireTabActivated(9, 1);
    await settleListeners();
    const firstStartedAt = store.currentView.startedAt;

    // 40 秒後に同じタブで B へ遷移する。
    now += 40_000;
    visibleTab = { id: 9, windowId: 1, url: urlB, title: "B" };
    fireTabUpdated(9, { url: urlB });
    await settleListeners();

    // さらに 20 秒後に A へ戻る (再表示)。
    now += 20_000;
    visibleTab = { id: 9, windowId: 1, url: urlA, title: "A" };
    fireTabUpdated(9, { url: urlA });
    await settleListeners();
    assert.equal(store.currentView.url, urlA);
    assert.equal(store.currentView.startedAt, now,
      "再表示が前の区間の続き扱いになっている");

    // 30 秒後にブラウザから離れて、2回目の A も確定させる。
    now += 30_000;
    visibleTab = null;
    fireFocusChanged(-1);
    await settleListeners();

    const views = (store.queue || []).filter((event) => event.event_type === "browser_view");
    assert.deepEqual(views.map((event) => event.payload.url), [urlA, urlB, urlA],
      "A→B→A の3区間にならなかった");
    const aViews = views.filter((event) => event.payload.url === urlA);
    assert.notEqual(aViews[0].event_id, aViews[1].event_id,
      "再表示が同じ event_id になり、受け口で1件に畳まれてしまう");
    assert.equal(aViews[0].end_time, new Date(firstStartedAt + 40_000).toISOString(),
      "1回目の A が B へ遷移した時刻で閉じられていない");
    assert.equal(aViews[1].start_time, new Date(firstStartedAt + 60_000).toISOString(),
      "2回目の A が再表示の時刻から始まっていない");
  } finally {
    Date.now = realDateNow;
    visibleTab = null;
  }
});

test("タイトルが後から確定したとき、閲覧中の区間を開き直さずタイトルだけ更新する", async () => {
  // 読み込み直後のタブは title が空のまま閲覧が始まることがある。
  // 後から届く onUpdated (title) で拾い直さないと browser_view が空タイトルで
  // 残り、逆に開き直してしまうと1つの閲覧が分断される。
  store = {};
  const realDateNow = Date.now;
  try {
    let now = realDateNow();
    Date.now = () => now;
    visibleTab = { id: 11, windowId: 1, url: "https://example.com/slow", title: "" };
    fireTabActivated(11, 1);
    await settleListeners();
    const openedAt = now;
    assert.equal(store.currentView.title, "", "開始時点ではタイトルが未確定のはず");

    // 10 秒後にタイトルが確定して onUpdated が届く。
    now += 10_000;
    visibleTab = { id: 11, windowId: 1, url: "https://example.com/slow", title: "遅れて確定したタイトル" };
    fireTabUpdated(11, { title: "遅れて確定したタイトル" });
    await settleListeners();

    assert.equal(store.currentView.title, "遅れて確定したタイトル",
      "閲覧中のビューのタイトルが更新されていない");
    assert.equal(store.currentView.startedAt, openedAt,
      "タイトルの確定で区間が開き直された (1つの閲覧が分断される)");
    assert.equal((store.queue || []).length, 0,
      "タイトルの確定だけでイベントができた");

    // 20 秒後にタブを閉じると、確定後のタイトルで1本の browser_view になる。
    now += 20_000;
    visibleTab = null;
    await fireTabRemoved(11);

    const views = (store.queue || []).filter((event) => event.event_type === "browser_view");
    assert.equal(views.length, 1, "閲覧が1本に確定されていない");
    assert.equal(views[0].payload.title, "遅れて確定したタイトル");
    assert.equal(views[0].start_time, new Date(openedAt).toISOString(),
      "区間の開始が最初に開いた時刻のまま保たれていない");
  } finally {
    Date.now = realDateNow;
    visibleTab = null;
  }
});
