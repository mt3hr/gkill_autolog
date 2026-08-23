// gkill autolog の Service Worker。
//
// 役割は2つ。
//   1. いま見えているタブ（フォーカスされたウィンドウのアクティブタブ）の
//      閲覧区間を browser_view イベントとして記録する。
//   2. content_media.js から届く再生実績を media_play イベントとして記録する。
//
// MV3 の Service Worker はいつでも停止されるため、状態とキューは
// すべて chrome.storage へ置き、メモリ上には持たない。

// 編集前に読む: .claude/skills/autolog-chrome-ext/SKILL.md（この領域の不変条件の正本）

import { DEFAULT_ENDPOINT, KEY_QUEUE, KEY_SETTINGS } from "./shared.js";

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

// FLUSH_CHUNK は1回の送信に載せる件数の上限。
// キューが上限まで育っても、受け口の本文上限 (8MB) に収まるように分けて送る。
const FLUSH_CHUNK = 200;

const KEY_VIEW = "currentView";
const KEY_PENDING_PLAYS = "pendingPlays";
const KEY_FINALIZED_PLAYS = "finalizedPlays";

// FINALIZED_TTL_MS は確定済み再生の記録 (墓標) を覚えておく長さ。
// 墓標は「同じ playId の報告が確定後も続いたとき」の差分計算にだけ要る。
// 長い動画を見続ける1日は十分に覆い、無限には溜めない。
const FINALIZED_TTL_MS = 24 * 60 * 60 * 1000;

// ---------------------------------------------------------------- 直列化
//
// chrome.storage の読み書きは get → 変更 → set の3手で、原子的ではない。
// イベントリスナ (タブ閉鎖・メッセージ・アラーム) は await の切れ目で交錯するため、
// 2つの処理が同じスナップショットを読むと、後勝ちで片方の変更が黙って消える。
// 再生中のタブを閉じると閲覧の確定 (onRemoved) と再生の確定 (pagehide の報告) が
// ほぼ同時に走るので、現実に踏む。
//
// Service Worker は単一インスタンスなので、状態を触る処理をメモリ上の
// Promise チェーンで1列に並べれば足りる。SW が止まればチェーンごと消えるが、
// そのとき実行中の処理も一緒に止まるので取り残しは起きない。
//
// ロックの中から withState を呼ぶとデッドロックする。状態を触る関数
// (syncView / closeView / recordProgress / finalizePlays / enqueue) は
// ロックを取らず、入口 (イベントリスナ) だけで取る。
// 例外は removeFromQueue で、ロックの外で走る flush から呼ばれるため自分で取る。
let stateChain = Promise.resolve();

function withState(task) {
  const run = stateChain.then(task, task);
  stateChain = run.then(() => {}, () => {});
  return run;
}

// ---------------------------------------------------------------- 設定

async function loadSettings() {
  const stored = await chrome.storage.local.get(KEY_SETTINGS);
  const settings = stored[KEY_SETTINGS] || {};
  return {
    endpoint: settings.endpoint || DEFAULT_ENDPOINT,
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

// flushing は flush の多重実行を防ぐ。キューが長いと1回の flush が
// 次の心拍をまたぐことがある。Service Worker が止まれば flush も一緒に
// 止まるので、メモリ上のフラグで足りる。
let flushing = false;

// flush はキューを先頭から分けて送る。
// 送信できたものだけをキューから外すので、収集側が落ちていても失われない。
//
// 受け口はバッチに1件でも不正なイベントがあると全体を 400 で拒む。
// そのまま同じバッチを再送し続けると、以後のイベントがすべて詰まる。
// 4xx のときは件数を半分に絞って原因のイベントを追い込み、1件まで絞れたら
// それを捨てて前へ進む。5xx とネットワークエラーは次の心拍で再試行する。
async function flush() {
  if (flushing) {
    return;
  }
  flushing = true;
  try {
    const { endpoint, token } = await loadSettings();
    if (!token) {
      return;
    }

    let sendCount = FLUSH_CHUNK;
    for (;;) {
      const stored = await chrome.storage.local.get(KEY_QUEUE);
      const queue = stored[KEY_QUEUE] || [];
      if (queue.length === 0) {
        return;
      }
      const batch = queue.slice(0, Math.min(sendCount, queue.length));

      let response;
      try {
        response = await fetch(endpoint, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ events: batch }),
        });
      } catch (error) {
        // 収集プログラムが動いていないだけ。次の心拍で再試行する。
        console.debug("gkill autolog: 送信できなかった", error);
        return;
      }

      if (response.ok) {
        await removeFromQueue(batch);
        sendCount = FLUSH_CHUNK;
        continue;
      }
      if (response.status === 401 || response.status === 403) {
        // トークンの設定違い。イベントに罪はないので捨てずに待つ。
        console.warn("gkill autolog: 認証に失敗した。オプションのトークンを確認", response.status);
        return;
      }
      if (response.status === 400) {
        // 受け口がイベントを拒んだ (不正なイベントが混ざっている)。
        // イベントが原因と確定できるのは受け口の 400 だけ。
        if (batch.length === 1) {
          console.warn("gkill autolog: 受け付けられないイベントを捨てた",
            response.status, await response.text(), batch[0]);
          await removeFromQueue(batch);
          sendCount = FLUSH_CHUNK;
          continue;
        }
        // どのイベントが原因か分からない。半分に絞って送り直す。
        sendCount = Math.ceil(batch.length / 2);
        continue;
      }
      if (response.status < 500) {
        // 400 以外の 4xx はイベントではなく環境の問題。
        // 送信先のパスを打ち間違えて別のサーバが 404/405 を返している場合など。
        // ここで二分破棄に入ると、設定を直す前にキューが空になってしまう。
        console.warn("gkill autolog: 送信先がイベントを受け付けない。送信先の設定を確認",
          response.status, await response.text());
        return;
      }
      console.warn("gkill autolog: 送信に失敗した", response.status, await response.text());
      return;
    }
  } finally {
    flushing = false;
  }
}

// removeFromQueue は送り終えた（または捨てた）イベントをキューから外す。
//
// 件数ではなく event_id で外す。送信中に enqueue の上限切り捨てが走ると
// 先頭の位置がずれ、件数で外すと未送信のイベントを巻き込んでしまう。
//
// flush はロックの外で走る (送信中に状態の処理を止めないため) ので、
// キューの読み書きだけここでロックを取る。並行した enqueue を上書きで消さない。
async function removeFromQueue(sent) {
  const sentIds = new Set(sent.map((event) => event.event_id));
  await withState(async () => {
    const stored = await chrome.storage.local.get(KEY_QUEUE);
    const queue = stored[KEY_QUEUE] || [];
    await chrome.storage.local.set({
      [KEY_QUEUE]: queue.filter((event) => !sentIds.has(event.event_id)),
    });
  });
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
// 30 秒未満の除外や不要ページの判定は取り込み時の normalize が行うため、ここでは絞らない。
async function closeView(now) {
  const stored = await chrome.storage.local.get(KEY_VIEW);
  const view = stored[KEY_VIEW];
  if (!view) {
    return;
  }

  // Service Worker が止まっていた場合、現在時刻は終了時刻として信用できない。
  const endedAt = now - view.heartbeatAt > STALE_MS ? view.heartbeatAt : now;
  if (endedAt <= view.startedAt) {
    await chrome.storage.local.remove(KEY_VIEW);
    return;
  }

  // イベントを積んでから区間を消す。逆順だと、消してから積むまでの間に
  // SW が落ちたとき区間ごと失われる。この順なら、積んだ直後に落ちても
  // 次の closeView が同じ決定的 event_id を積み直すだけで、
  // 受け口の (device, event_id) が1件に畳む。
  await enqueue({
    schema_version: 1,
    // 同じ閲覧区間からは同じ id を作る。syncView は複数のイベント
    // (タブ切替・URL遷移・心拍) から並行に呼ばれることがあり、
    // 同じ区間を2回積んでも受け口の (device, event_id) で1件に畳まれる。
    event_id: `browser_view:${view.tabId}:${view.startedAt}`,
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
  await chrome.storage.local.remove(KEY_VIEW);
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
    // 心拍が長く途絶えていたら、Service Worker ごと止まっていた (スリープなど)。
    // 同じページが見えていても連続した閲覧ではないので、最後の心拍の時刻で
    // 区間を閉じてから開き直す。ここで継続扱いにすると、眠っていた時間が
    // 「閲覧中」として記録され、閉じたときの補修も心拍の上書きで効かなくなる。
    if (now - view.heartbeatAt > STALE_MS) {
      await closeView(now);
      await openView(tab, now);
      return;
    }
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
  withState(syncView);
});

chrome.runtime.onStartup.addListener(() => {
  chrome.alarms.create(ALARM_NAME, { periodInMinutes: TICK_MINUTES });
  withState(syncView);
});

// Service Worker が起きるたびの保険。アラームが何かの拍子に消えていると
// 心拍が止まり、閲覧の終了もキューの送信も行われなくなる。
chrome.alarms.get(ALARM_NAME).then((alarm) => {
  if (!alarm) {
    chrome.alarms.create(ALARM_NAME, { periodInMinutes: TICK_MINUTES });
  }
});

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name !== ALARM_NAME) {
    return;
  }
  await withState(async () => {
    await syncView();
    await finalizePlays(Date.now());
  });
  // flush はロックの外。キューが長いと送信が心拍をまたぐことがあり、
  // その間も閲覧・再生の記録を止めない。キューの取り外しは removeFromQueue が
  // 自分でロックを取る。
  await flush();
});

chrome.tabs.onActivated.addListener(() => {
  withState(syncView);
});

chrome.tabs.onUpdated.addListener((tabId, changeInfo) => {
  // URL の遷移とタイトルの確定の両方を拾う。
  if (changeInfo.url || changeInfo.title) {
    withState(syncView);
  }
});

chrome.tabs.onRemoved.addListener((tabId) => {
  return withState(async () => {
    const stored = await chrome.storage.local.get(KEY_VIEW);
    if (stored[KEY_VIEW] && stored[KEY_VIEW].tabId === tabId) {
      await closeView(Date.now());
    }
  });
});

chrome.windows.onFocusChanged.addListener(() => {
  withState(syncView);
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
  // 失敗しても必ず応答する。返さないままにすると、content script 側の
  // sendMessage が「応答の前に接続が閉じた」で終わり、原因が分からなくなる。
  withState(() => recordProgress(message))
    .then(() => sendResponse({ received: true }))
    .catch((error) => {
      console.error("gkill autolog: 再生の記録に失敗した", error);
      sendResponse({ received: false });
    });
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

  const stored = await chrome.storage.local.get([KEY_PENDING_PLAYS, KEY_FINALIZED_PLAYS]);
  const pending = stored[KEY_PENDING_PLAYS] || {};
  const finalized = stored[KEY_FINALIZED_PLAYS] || {};
  const previous = pending[message.playId];

  // content script の playedSeconds は累計で、確定後もリセットされない
  // (スリープ復帰などで報告が途絶えて心拍が確定させても、ページ側は続きを数える)。
  // 確定済みの分を引かずにそのまま積むと、同じ再生時間が二重に記録される。
  // 確定した時点の値を墓標として覚えておき、以後の報告は差分に組み替える。
  let playedSeconds = message.playedSeconds;
  let startedAt = message.startedAt;
  const tombstone = finalized[message.playId];
  if (tombstone) {
    playedSeconds = message.playedSeconds - tombstone.playedSeconds;
    if (playedSeconds <= 0) {
      // 確定済みの内容の再送。新しい再生は進んでいない。
      // 墓標の時刻だけ更新する。掃除 (FINALIZED_TTL_MS) を「最後に報告を
      // 見てから」で数えないと、タブを開いたまま TTL を超えて一時停止した
      // 再生が再開したとき墓標が消えていて、累計の全量が新しい再生として
      // 二重計上されてしまう。
      tombstone.finalizedAt = Date.now();
      await chrome.storage.local.set({ [KEY_FINALIZED_PLAYS]: finalized });
      return;
    }
    // 続きの区間は、確定した区間の終わりから始まったとみなす。
    // 実際の再開時刻は分からないが、区間の重なりと二重計上は避けられる。
    startedAt = Math.max(tombstone.endedAt, message.startedAt);
  }

  pending[message.playId] = {
    service: message.service,
    url: message.url || "",
    videoId: message.videoId,
    // 確定済みのタイトルを空で潰さない。
    // MediaSession がまだ返さない間の報告が後から届くことがある。
    title: message.title || (previous ? previous.title : "") || "",
    artist: message.artist || (previous ? previous.artist : "") || "",
    playedSeconds,
    startedAt,
    endedAt: message.endedAt,
    updatedAt: Date.now(),
    // 再生が終わったことを content script が知っている場合は即座に確定させる。
    finished: Boolean(message.finished),
  };

  await chrome.storage.local.set({ [KEY_PENDING_PLAYS]: pending });
  await finalizePlays(Date.now());
}

// finalizePlays は報告が途切れた再生を media_play イベントとして確定させる。
//
// 確定した再生は墓標 (finalizedPlays) に累計値を残す。同じ playId の報告が
// 確定後も続いた場合、recordProgress が墓標との差分だけを続きの再生として扱う。
// これが無いと、スリープ復帰などで一度確定した再生が再開されたとき、
// 累計の実再生秒数を持つ2本目のイベントができて二重計上になる。
async function finalizePlays(now) {
  const stored = await chrome.storage.local.get([KEY_PENDING_PLAYS, KEY_FINALIZED_PLAYS]);
  const pending = stored[KEY_PENDING_PLAYS] || {};
  const finalized = stored[KEY_FINALIZED_PLAYS] || {};

  // 使い終わった墓標を掃除する。差分計算に要るのは、その playId の報告を
  // 最後に見てからしばらくの間だけ (再送のたびに finalizedAt を更新している)。
  for (const [playId, tombstone] of Object.entries(finalized)) {
    if (now - tombstone.finalizedAt > FINALIZED_TTL_MS) {
      delete finalized[playId];
    }
  }

  const remaining = {};
  for (const [playId, play] of Object.entries(pending)) {
    if (!play.finished && now - play.updatedAt <= STALE_MS) {
      remaining[playId] = play;
      continue;
    }
    if (play.playedSeconds <= 0) {
      continue;
    }
    if (!(play.endedAt > play.startedAt)) {
      // 時計の巻き戻り等。終了が開始より前のイベントは受け口が拒むため作らない。
      continue;
    }
    await enqueue({
      schema_version: 1,
      // 同じ再生の確定からは同じ id を作る。finalizePlays は心拍と報告受信の
      // 両方から並行に呼ばれることがあり、同じ確定を2回積んでも受け口の
      // (device, event_id) で1件に畳まれる。確定後に同じ再生が続いた場合は
      // 差分の区間 (開始が前回の終了) になるので別 id になる。
      event_id: `media_play:${playId}:${play.startedAt}:${Math.round(play.endedAt / 1000)}`,
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

    // 墓標は content script の累計値の座標で持つ。
    // 差分に組み替えた再生 (2本目以降) の確定では、前回の累計へ差分を足す。
    const previousTotal = finalized[playId] ? finalized[playId].playedSeconds : 0;
    finalized[playId] = {
      playedSeconds: previousTotal + play.playedSeconds,
      endedAt: play.endedAt,
      finalizedAt: now,
    };
  }

  await chrome.storage.local.set({
    [KEY_PENDING_PLAYS]: remaining,
    [KEY_FINALIZED_PLAYS]: finalized,
  });
}
