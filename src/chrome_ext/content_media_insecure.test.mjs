// 平文 http のページ (secure context でない) には crypto.randomUUID が無い。
// それでも再生の計測が動くことを検証する。
//
// 実行: npm run test_chrome_ext

import test from "node:test";
import assert from "node:assert/strict";

let mediaElements = [];
const sentMessages = [];
const pageListeners = {};
let tickFn = null;

globalThis.location = { hostname: "media.local", href: "http://media.local/video/1" };
globalThis.document = {
  title: "宅内の動画",
  querySelectorAll: () => mediaElements,
  querySelector: () => null,
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
// randomUUID の無い crypto (平文 http と同じ状態)。
Object.defineProperty(globalThis, "crypto", {
  value: {
    getRandomValues: (array) => {
      for (let i = 0; i < array.length; i++) {
        array[i] = (i * 37 + 11) % 256;
      }
      return array;
    },
  },
  configurable: true,
});

await import("./content_media.js");

test("crypto.randomUUID の無い平文 http ページでも再生を記録できる", () => {
  const video = { loop: false, muted: false, duration: 600, paused: false, ended: false, currentTime: 0, playbackRate: 1 };
  mediaElements = [video];
  for (const fn of pageListeners.play) {
    fn();
  }
  assert.ok(tickFn, "ticker が動いていない");

  for (let played = 0; played < 35; played += 0.9) {
    video.currentTime += 0.9;
    tickFn();
  }
  for (const fn of pageListeners.pagehide) {
    fn();
  }

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "再生が記録されていない (playId の生成で落ちている)");
  assert.ok(finished[0].playId, "playId が空");
  assert.equal(finished[0].service, "web");
  assert.equal(finished[0].url, "http://media.local/video/1");
});
