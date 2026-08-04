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

function fireVisibilityChange() {
  for (const fn of pageListeners.visibilitychange || []) {
    fn();
  }
}

// seconds 秒ぶん再生を進める。1 tick で 0.9 秒進める (maxDelta 1 秒の内側)。
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

test("一時停止中は currentTime が動いても加算されず、再開後の分だけ数える", () => {
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 5); // 約4.5秒ぶん

  // 一時停止。停止中にシークバーを動かすと currentTime だけが進む。
  // 1 tick あたり 0.9 秒 (maxDelta の内側) 進めるので、paused の判定が
  // 無ければそのまま加算されてしまう動きになっている。
  video.paused = true;
  for (let i = 0; i < 10; i++) {
    video.currentTime += 0.9;
    tickFn();
  }

  // 再開。停止中も位置だけは追っているので、飛びとみなされず続きから数える。
  video.paused = false;
  playFor(video, 5); // 約5.4秒ぶん

  firePagehide();

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "確定が1回でない");
  assert.ok(finished[0].playedSeconds > 9 && finished[0].playedSeconds < 11,
    `一時停止中の分が加算された (playedSeconds=${finished[0].playedSeconds})`);
  const playIds = new Set(sentMessages.map((message) => message.playId));
  assert.equal(playIds.size, 1, "一時停止で再生が分断された");
});

test("シークで飛んだ分は前方も後方も加算されず、再生も分断されない", () => {
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 5); // 約4.5秒

  // 前方へ大きくシーク。1 tick の実時間 (maxDelta ≒ 1秒) を大きく超えるので
  // 棄却される。棄却後も位置を追い直し、続きの再生は数えられること。
  video.currentTime += 300;
  tickFn();

  playFor(video, 5); // 約5.4秒

  // 後方へシーク。負の delta も加算しない。
  video.currentTime -= 200;
  tickFn();

  playFor(video, 3); // 約3.6秒

  firePagehide();

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "確定が1回でない");
  assert.ok(finished[0].playedSeconds > 12.5 && finished[0].playedSeconds < 14.5,
    `シークの飛び分が加算された、または棄却後の再生が数えられていない (playedSeconds=${finished[0].playedSeconds})`);
  const playIds = new Set(sentMessages.map((message) => message.playId));
  assert.equal(playIds.size, 1, "シークで再生が分断された");
});

test("ループ再生の素材は数えない", () => {
  // 背景で流れる装飾目的の映像を視聴として残さないため。
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  video.loop = true;
  mediaElements = [video];
  firePlay();
  assert.ok(tickFn, "ticker が動いていない");

  // 数える対象の要素が無いので、最初の tick で ticker ごと止まる。
  video.currentTime += 0.9;
  tickFn();
  assert.equal(tickFn, null, "ループ素材なのに計測が続いている");

  firePagehide();
  assert.equal(sentMessages.length, 0, "ループ素材の再生が報告された");
});

test("30秒未満の素材は数えず、ちょうど30秒と長さ不明 (ライブ配信) は数える", () => {
  // 除外は 30 秒「未満」が境界。短い装飾素材を除きたいだけなので、
  // ちょうど 30 秒は数え、長さの取れないライブ配信も数える。
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";

  // 29.9秒: 対象外。
  const short = newVideo();
  short.duration = 29.9;
  mediaElements = [short];
  firePlay();
  short.currentTime += 0.9;
  tickFn();
  assert.equal(tickFn, null, "30秒未満の素材なのに計測が続いている");
  firePagehide();
  assert.equal(sentMessages.length, 0, "30秒未満の素材の再生が報告された");

  // ちょうど30秒: 対象。
  reset();
  const exact = newVideo();
  exact.duration = 30;
  mediaElements = [exact];
  firePlay();
  playFor(exact, 2);
  firePagehide();
  let finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "ちょうど30秒の素材が数えられていない (境界がずれている)");
  assert.ok(finished[0].playedSeconds > 0, "ちょうど30秒の素材の再生時間が空");

  // 長さ不明 (Infinity): 対象。
  reset();
  const live = newVideo();
  live.duration = Infinity;
  mediaElements = [live];
  firePlay();
  playFor(live, 2);
  firePagehide();
  finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "ライブ配信 (長さ不明) が数えられていない");
});

test("YouTube では消音でも数える (消音の除外は一般サイトだけ)", () => {
  // このハーネスのホストは www.youtube.com。YouTube で音を切るのは利用者が
  // 意図してやったことで、視聴していないとは言えないため計測の対象になる。
  // muted の除外条件に !isYouTubeFamily が付いていることの検証。
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  video.muted = true;
  mediaElements = [video];
  firePlay();
  playFor(video, 5);
  firePagehide();

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "YouTube の消音再生が数えられていない");
  assert.ok(finished[0].playedSeconds > 4,
    `YouTube の消音再生の秒数が足りない (playedSeconds=${finished[0].playedSeconds})`);
});

test("動画IDが変わったら前の再生を確定し、新しい playId とそのとき取り込んだタイトルで数え直す", () => {
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  globalThis.document.title = "動画A - YouTube";
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 35); // 約34.2秒

  // 自動再生で次の動画へ。SPA なので URL・タイトル・再生位置が変わるだけで、
  // <video> 要素は同じものが使い回される。
  globalThis.location.href = "https://www.youtube.com/watch?v=zyxwvutsrqp";
  globalThis.document.title = "動画B - YouTube";
  video.currentTime = 0;
  tickFn(); // この tick で前の再生が確定し、新しい再生が始まる

  playFor(video, 20); // 約20.7秒
  firePagehide();

  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 2, "切り替えで前の再生が確定されていない");

  // 前の再生は、計測中に取り込んだタイトルと URL のまま確定すること。
  // 切り替わりに気づくのは切り替わった後なので、送信時に DOM を読むと
  // 次の動画のタイトルが前の再生に載ってしまう (ヘッダの約束)。
  assert.equal(finished[0].videoId, "abcdefghijk");
  assert.equal(finished[0].url, "https://www.youtube.com/watch?v=abcdefghijk");
  assert.equal(finished[0].title, "動画A - YouTube", "前の再生に次の動画のタイトルが載った");
  assert.ok(finished[0].playedSeconds > 30 && finished[0].playedSeconds < 36,
    `切り替え前の秒数が正しくない (playedSeconds=${finished[0].playedSeconds})`);

  // 新しい再生は別の playId で、秒数もゼロから数え直すこと。
  assert.equal(finished[1].videoId, "zyxwvutsrqp");
  assert.equal(finished[1].title, "動画B - YouTube");
  assert.notEqual(finished[1].playId, finished[0].playId, "playId が使い回された");
  assert.ok(finished[1].playedSeconds > 18 && finished[1].playedSeconds < 25,
    `前の再生の秒数を引き継いだ (playedSeconds=${finished[1].playedSeconds})`);

  globalThis.document.title = "動画タイトル - YouTube";
});

test("動画IDを確認できないホームのプレビュー再生は数えず、進行中の再生はそこで確定する", () => {
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 35);

  // ホームへ戻り、サムネイルのプレビューが鳴っている。URL に動画IDが無く
  // MediaSession も無いので、何を再生しているのか確認できない。
  globalThis.location.href = "https://www.youtube.com/";
  video.currentTime += 0.9;
  tickFn();

  // 進行中だった再生は (media 消失の猶予とは別に) この tick で確定すること。
  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "ホームへ戻っても再生が確定されない");
  assert.equal(finished[0].videoId, "abcdefghijk");

  // プレビューが鳴り続けても、新しい再生は始まらず何も報告されないこと。
  // ページの URL で記録すると、見ていない動画の再生まで残ってしまう。
  const count = sentMessages.length;
  for (let i = 0; i < 10; i++) {
    video.currentTime += 0.9;
    tickFn();
  }
  firePagehide();
  assert.equal(sentMessages.length, count, "ホームのプレビュー再生が報告された");
});

test("タブが隠れたら途中報告を送り、計測は同じ playId のまま続く", () => {
  reset();
  globalThis.location.href = "https://www.youtube.com/watch?v=abcdefghijk";
  const video = newVideo();
  mediaElements = [video];
  firePlay();

  playFor(video, 5); // 約4.5秒

  // タブが隠れた。タブが突然閉じられても実績が残るよう、途中経過を送る。
  const before = sentMessages.length;
  globalThis.document.visibilityState = "hidden";
  fireVisibilityChange();
  assert.equal(sentMessages.length, before + 1, "隠れたときに途中報告が送られない");
  const interim = sentMessages[sentMessages.length - 1];
  assert.equal(interim.finished, false, "途中報告が確定として送られた");
  assert.ok(interim.playedSeconds > 4 && interim.playedSeconds < 5,
    `途中報告の秒数が正しくない (playedSeconds=${interim.playedSeconds})`);

  // 表示に戻っただけでは何も送らない。
  globalThis.document.visibilityState = "visible";
  fireVisibilityChange();
  assert.equal(sentMessages.length, before + 1, "表示に戻ったときにも報告が送られた");

  // pagehide と違い play は捨てられず、同じ playId のまま累計が続くこと。
  playFor(video, 5);
  firePagehide();
  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "確定が1回でない");
  assert.equal(finished[0].playId, interim.playId, "隠れた後に playId が変わった (再生が分断された)");
  assert.ok(finished[0].playedSeconds > 9,
    `隠れた後の再生が数えられていない (playedSeconds=${finished[0].playedSeconds})`);
});

// このテストはモジュールを別ホストで読み直すので、必ずファイルの最後に置く。
// content_media.js はモジュール先頭で hostname を確定するため、YouTube 系以外の
// 挙動は location を差し替えてからクエリ付き URL で再読込した2つ目の
// インスタンスで検証する。グローバルイベントは新旧両方に届くが、旧インスタンスの
// play は確定済みで、tick も新インスタンスのものに入れ替わるので干渉しない。
test("一般サイトでは消音の自動再生を数えない", async () => {
  reset();
  globalThis.location = { hostname: "example.com", href: "https://example.com/podcast" };
  await import("./content_media.js?host=generic");

  // 一般のサイトでは消音の自動再生が装飾の目印なので数えない。
  const muted = newVideo();
  muted.muted = true;
  mediaElements = [muted];
  firePlay();
  assert.ok(tickFn, "ticker が動いていない");
  muted.currentTime += 0.9;
  tickFn();
  assert.equal(tickFn, null, "消音の自動再生なのに計測が続いている");
  firePagehide();
  assert.equal(sentMessages.length, 0, "一般サイトの消音再生が報告された");

  // 音が出ていれば同じページでも数える (除外の理由が muted であることの対照)。
  const audible = newVideo();
  mediaElements = [audible];
  firePlay();
  playFor(audible, 5);
  firePagehide();
  const finished = sentMessages.filter((message) => message.finished);
  assert.equal(finished.length, 1, "一般サイトの再生が数えられていない");
  assert.equal(finished[0].service, "web");
  assert.equal(finished[0].url, "https://example.com/podcast");
  assert.ok(finished[0].playedSeconds > 4,
    `一般サイトの再生の秒数が足りない (playedSeconds=${finished[0].playedSeconds})`);
});
