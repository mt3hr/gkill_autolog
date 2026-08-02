// content_media.js (再生計測) を Node 上で動かして検証する。
//
// DOM の完全な再現ではない。計測ロジックが見る範囲
// (querySelectorAll・広告クラス・MediaSession・タイマー) だけをスタブする。
// tick は setInterval に頼らず手で回す。壁時計はほぼ進まないので、
// maxDelta (wallDelta+1) の制約から1 tick あたり 1 秒未満の前進だけが数えられる。
//
// 実行: npm run test_chrome_ext

import test from "node:test";
import assert from "node:assert/strict";

// ---- content_media.js が触るグローバルのスタブ (import より先に用意する) ----

// ページ上の <video>/<audio>。テストが差し替える。
let mediaElements = [];
// YouTube の広告表示中クラスがあるか。
let adShowing = false;
// Service Worker へ送られた報告。
const sentMessages = [];
// addEventListener で登録されたハンドラ。
const pageListeners = {};
// setInterval で登録された tick。
let tickFn = null;

globalThis.location = { hostname: "www.youtube.com", href: "https://www.youtube.com/watch?v=abcdefghijk" };
globalThis.document = {
  title: "動画タイトル - YouTube",
  querySelectorAll: () => mediaElements,
  querySelector: (selector) =>
    selector.includes("ad-showing") && adShowing ? {} : null,
};
try {
  Object.defineProperty(globalThis, "navigator", {
    value: { mediaSession: null },
    configurable: true,
  });
} catch {
  globalThis.navigator.mediaSession = null;
}
globalThis.addEventListener = (type, fn) => {
  (pageListeners[type] = pageListeners[type] || []).push(fn);
};
globalThis.setInterval = (fn) => {
  tickFn = fn;
  return 1;
};
globalThis.clearInterval = () => {
  tickFn = null;
};
globalThis.chrome = {
  runtime: {
    sendMessage: async (message) => {
      sentMessages.push(structuredClone(message));
      return { received: true };
    },
  },
};

await import("./content_media.js");

function newVideo() {
  return { loop: false, muted: false, duration: 600, paused: false, ended: false, currentTime: 0, playbackRate: 1 };
}

function firePlay() {
  for (const fn of pageListeners.play || []) {
    fn();
  }
}

function firePagehide() {
  for (const fn of pageListeners.pagehide || []) {
    fn();
  }
}

// playSeconds 秒ぶん再生を進める。1 tick で 0.9 秒進める (maxDelta 1 秒の内側)。
function playFor(video, seconds) {
  for (let played = 0; played < seconds; played += 0.9) {
    video.currentTime += 0.9;
    tickFn();
  }
}

function reset() {
  mediaElements = [];
  adShowing = false;
  sentMessages.length = 0;
  // 進行中の play を終わらせて掃除する (対象なし + 猶予切れで確定する)。
  if (tickFn) {
    for (let i = 0; i < 5 && tickFn; i++) {
      tickFn();
    }
  }
  sentMessages.length = 0;
}

test("30秒未満の広告で計測対象が見えなくなっても、1本の再生として数え続ける", () => {
  reset();
  const video = newVideo();
  mediaElements = [video];
  firePlay();
  assert.ok(tickFn, "ticker が動いていない");

  // 40秒視聴 → 広告 (要素が countable でなくなる) → 40秒視聴。
  playFor(video, 40);
  mediaElements = [];
  adShowing = true;
  for (let i = 0; i < 20; i++) {
    tickFn(); // 20秒の広告。何 tick でも待つ
  }
  assert.ok(tickFn, "広告中に ticker が止まった");
  adShowing = false;
  mediaElements = [video];
  playFor(video, 40);

  // ページを離れて確定。
  firePagehide();

  const playIds = new Set(sentMessages.map((message) => message.playId));
  assert.equal(playIds.size, 1, "広告で再生が分断された (playId が複数できた)");

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "確定が複数回起きた");
  assert.ok(finished[0].playedSeconds > 70,
    `広告の前後が合算されていない (playedSeconds=${finished[0].playedSeconds})`);
});

test("広告でなく対象が消えた場合は、猶予のあとで確定する", () => {
  reset();
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 35);

  // 一覧へ戻るなどで対象が消えた。広告表示ではない。
  mediaElements = [];
  tickFn(); // 猶予 1
  tickFn(); // 猶予 2
  assert.equal(sentMessages.filter((message) => message.finished).length, 0,
    "猶予中に確定された");
  tickFn(); // 猶予切れ → 確定
  assert.equal(sentMessages.filter((message) => message.finished).length, 1,
    "猶予が切れても確定されない");
  assert.equal(tickFn, null, "確定後に ticker が止まっていない");
});

test("pagehide の確定後に再生が続いたら、新しい playId で数え直す", () => {
  reset();
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 35);
  const firstIds = new Set(sentMessages.map((message) => message.playId));
  assert.equal(firstIds.size, 1);

  // ページを離れる (bfcache に入る)。play は捨てられる。
  firePagehide();

  // 復元されて再生が続く。
  firePlay();
  playFor(video, 35);
  firePagehide();

  const playIds = new Set(sentMessages.map((message) => message.playId));
  assert.equal(playIds.size, 2, "復元後の再生が同じ playId のまま (累計の二重計上になる)");
});
